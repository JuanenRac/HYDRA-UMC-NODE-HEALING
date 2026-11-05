// HYDRA-UMC-NODE-HEALING - watchdog package: events.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// What the watchdog tells the rest of the ecosystem, off by default and only when --events-config names a file:
//
//   - every transition of a node, as an event envelope of the SDK (event-envelope.schema.json) on
//     <topicPrefix>/<node>/state, retained, so STUDIO, the watch, the phone and the dashboard can show it;
//   - after a node has stayed in a failing state for a time written in the file, ONE proposal of a maintenance incident
//     (maintenance-incident.schema.json) on <topicPrefix>/<node>/incident-proposal, not retained. It is only a proposal: the
//     watchdog opens nothing, changes nothing and stops nothing, and what turns it into an incident that a person
//     reviews belongs to the maintenance agent, which decides whether to listen to the topic.
//
// Publishing never delays the polling: a transition goes into a bounded queue that one worker empties, and a full
// queue drops the message and counts it.
package watchdog

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/redact"
)

// EventSource is the "source" of every event.
const EventSource = "hydra-umc-node-healing"

// EventTransition is the "type" of the event published for a change of state.
const EventTransition = "node.health.transition"

// EventsConfig is the file named by --events-config. Everything is off until the file turns it on.
type EventsConfig struct {
	MQTT              MQTTSection      `json:"mqtt"`
	IncidentProposals ProposalsSection `json:"incidentProposals"`
	// MissionState turns on the READING of the state of the mission queue that JOB-DISPATCHER keeps retained on the same broker.
	MissionState MissionStateSection `json:"missionState"`
}

// MissionStateSection says whether the watchdog reads the state of the missions (see missionstate.go). Read only, off by default.
type MissionStateSection struct {
	Enabled bool `json:"enabled"`
	// Topic defaults to hydra/jobs/state.
	Topic string `json:"topic"`
}

// MQTTSection says where the events go.
type MQTTSection struct {
	Enabled        bool   `json:"enabled"`
	Broker         string `json:"broker"`
	TLS            bool   `json:"tls"`
	CAFile         string `json:"caFile"`
	ClientID       string `json:"clientId"`
	UsernameFile   string `json:"usernameFile"`
	PasswordFile   string `json:"passwordFile"`
	AllowPlaintext bool   `json:"allowPlaintext"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	TopicPrefix    string `json:"topicPrefix"`
	QueueSize      int    `json:"queueSize"`
}

// ProposalsSection says when a node that stays down becomes a proposal.
type ProposalsSection struct {
	Enabled          bool `json:"enabled"`
	SustainedSeconds int  `json:"sustainedSeconds"`
	// IncludeDegraded also proposes for a node that answers but reports itself degraded or unhealthy-by-itself; off by default.
	IncludeDegraded bool `json:"includeDegraded"`
}

// DefaultEventsConfig is the file with nothing in it.
func DefaultEventsConfig() EventsConfig {
	return EventsConfig{
		MQTT:              MQTTSection{ClientID: "hydra-umc-node-healing", TimeoutSeconds: 5, TopicPrefix: "hydra/health", QueueSize: 64},
		IncidentProposals: ProposalsSection{SustainedSeconds: 60},
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate checks the values; it never reads a file.
func (c EventsConfig) Validate() error {
	m := c.MQTT
	if m.Enabled {
		host, port, err := net.SplitHostPort(m.Broker)
		if err != nil || host == "" || port == "" {
			return errors.New("mqtt.broker must be host:port")
		}
		if !m.TLS && !isLoopbackHost(host) && !m.AllowPlaintext {
			return fmt.Errorf("mqtt.broker %s is not on this machine: turn mqtt.tls on, or set mqtt.allowPlaintext to say on purpose that plain text is fine", m.Broker)
		}
		if m.CAFile != "" && !m.TLS {
			return errors.New("mqtt.caFile needs mqtt.tls")
		}
		if m.PasswordFile != "" && m.UsernameFile == "" {
			return errors.New("mqtt.passwordFile needs mqtt.usernameFile")
		}
		if m.ClientID == "" || len(m.ClientID) > 64 {
			return errors.New("mqtt.clientId must have 1 to 64 characters")
		}
		if m.TimeoutSeconds < 1 || m.TimeoutSeconds > 60 {
			return errors.New("mqtt.timeoutSeconds must be from 1 to 60")
		}
		if m.QueueSize < 1 || m.QueueSize > 10000 {
			return errors.New("mqtt.queueSize must be from 1 to 10000")
		}
		if m.TopicPrefix == "" || strings.HasPrefix(m.TopicPrefix, "/") || strings.HasSuffix(m.TopicPrefix, "/") || containsAny(m.TopicPrefix, "#+\x00") || len(m.TopicPrefix) > 200 {
			return errors.New("mqtt.topicPrefix must be a topic without wildcards, such as hydra/health")
		}
	}
	p := c.IncidentProposals
	if p.Enabled {
		if !m.Enabled {
			return errors.New("incidentProposals.enabled needs mqtt.enabled: the proposals travel over MQTT")
		}
		if p.SustainedSeconds < 1 || p.SustainedSeconds > 86400 {
			return errors.New("incidentProposals.sustainedSeconds must be from 1 to 86400")
		}
	}
	return nil
}

// LoadEventsConfig reads the file (strictly: an unknown key stops the start) and checks it.
func LoadEventsConfig(path string) (EventsConfig, error) {
	cfg := DefaultEventsConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return DefaultEventsConfig(), fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return DefaultEventsConfig(), fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func readSecretFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Target builds the broker of the file, reading the credential files.
func (c EventsConfig) Target() (MQTTTarget, error) {
	user, err := readSecretFile(c.MQTT.UsernameFile)
	if err != nil {
		return MQTTTarget{}, fmt.Errorf("cannot read mqtt.usernameFile: %w", err)
	}
	pass, err := readSecretFile(c.MQTT.PasswordFile)
	if err != nil {
		return MQTTTarget{}, fmt.Errorf("cannot read mqtt.passwordFile: %w", err)
	}
	return MQTTTarget{Broker: c.MQTT.Broker, TLS: c.MQTT.TLS, CAFile: c.MQTT.CAFile, ClientID: c.MQTT.ClientID, Username: user, Password: pass, Timeout: time.Duration(c.MQTT.TimeoutSeconds) * time.Second}, nil
}

// TopicSegment makes a node name safe to use as one level of a topic.
func TopicSegment(name string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == ':':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// label shortens a name to what the incident contract accepts (^[A-Za-z0-9._:-]{1,64}$).
func label(name string) string {
	s := TopicSegment(name)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// outgoing is one message waiting for the worker.
type outgoing struct {
	topic   string
	payload []byte
	retain  bool
}

// MultiReactor passes every transition to each of its reactors, in order.
type MultiReactor []Reactor

// OnTransition implements Reactor.
func (m MultiReactor) OnTransition(node Node, from, to Status, detail string) {
	for _, r := range m {
		if r != nil {
			r.OnTransition(node, from, to, detail)
		}
	}
}

// EventReactor publishes the events of EventsConfig. Use NewEventReactor and Run.
type EventReactor struct {
	Config    EventsConfig
	Publisher Publisher
	Printf    func(format string, args ...any)
	// Now defaults to time.Now.
	Now func() time.Time

	// sustainedOverride replaces the time of the file; tests only.
	sustainedOverride time.Duration

	queue    chan outgoing
	sequence atomic.Int64
	// counters for the status endpoint
	published atomic.Int64
	failed    atomic.Int64
	dropped   atomic.Int64
	proposals atomic.Int64

	mu       sync.Mutex
	episodes map[string]*episode // node name -> the failing state being watched for a proposal
}

type episode struct {
	timer *time.Timer
	state Status
}

// NewEventReactor builds the reactor; nothing is sent until Run starts the worker.
func NewEventReactor(cfg EventsConfig, publisher Publisher, printf func(format string, args ...any)) *EventReactor {
	size := cfg.MQTT.QueueSize
	if size < 1 {
		size = 64
	}
	return &EventReactor{Config: cfg, Publisher: publisher, Printf: printf, queue: make(chan outgoing, size), episodes: make(map[string]*episode)}
}

func (r *EventReactor) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *EventReactor) printf(format string, args ...any) {
	if r.Printf != nil {
		r.Printf(format, args...)
		return
	}
	fmt.Printf(format, args...)
}

// Published, Failed and Dropped count the messages for GET /metrics; Proposals counts the proposals queued.
func (r *EventReactor) Published() int64 { return r.published.Load() }
func (r *EventReactor) Failed() int64    { return r.failed.Load() }
func (r *EventReactor) Dropped() int64   { return r.dropped.Load() }
func (r *EventReactor) Proposals() int64 { return r.proposals.Load() }

func eventID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	return "evt-" + hex.EncodeToString(raw[:])
}

func clip(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) { // do not cut a character in two
		cut = cut[:len(cut)-1]
	}
	return cut
}

// failing says whether a state is one a proposal can follow.
func (r *EventReactor) failing(s Status) bool {
	switch s {
	case StatusUnreachable, StatusInvalid, StatusUnhealthy:
		return true
	case StatusDegraded:
		return r.Config.IncidentProposals.IncludeDegraded
	}
	return false
}

func (r *EventReactor) enqueue(m outgoing) {
	select {
	case r.queue <- m:
	default:
		r.dropped.Add(1)
	}
}

// StateEvent builds the event envelope of one transition.
func (r *EventReactor) StateEvent(node Node, from, to Status, detail string) []byte {
	event := map[string]any{
		"schema_version": "1.0",
		"event_id":       eventID(),
		"type":           EventTransition,
		"source":         EventSource,
		"timestamp_utc":  r.now().UTC().Format(time.RFC3339Nano),
		"sequence":       r.sequence.Add(1) - 1,
		"node":           node.Name,
		"from":           from.String(),
		"to":             to.String(),
		"detail":         clip(redact.Secrets(detail), 500),
	}
	data, _ := json.Marshal(event)
	return data
}

// Proposal builds the maintenance incident that is proposed for a node that stayed in `state` for `sustained`.
func (r *EventReactor) Proposal(node Node, state Status, detail string, sustained time.Duration) []byte {
	at := r.now().UTC()
	severity := "warning"
	if state == StatusUnreachable || state == StatusInvalid {
		severity = "critical"
	}
	seq := r.proposals.Load()
	symptom := fmt.Sprintf("%s has been %s for at least %d s", node.Name, state, int(sustained.Seconds()))
	if detail != "" {
		symptom += ": " + detail
	}
	incident := map[string]any{
		"schema_version": "1.0",
		"incidentId":     label(fmt.Sprintf("heal-%s-%d", node.Name, at.Unix())),
		"sourceNode":     label(node.Name),
		"detectedAt":     at.Format(time.RFC3339),
		"severity":       severity,
		"component":      clip(redact.Secrets(node.Name), 500),
		"symptom":        clip(redact.Secrets(symptom), 2000),
		"evidenceRefs":   []string{"mqtt:" + r.Config.MQTT.TopicPrefix + "/" + TopicSegment(node.Name) + "/state"},
		"redactionLevel": "sanitized",
		"requestedBy":    nil,
		"correlationId":  label(fmt.Sprintf("heal-%d", seq)),
	}
	data, _ := json.Marshal(incident)
	return data
}

// OnTransition implements Reactor: it queues the event of the transition and, when proposals are on, starts or ends the wait
// for a sustained failure. It never blocks.
func (r *EventReactor) OnTransition(node Node, from, to Status, detail string) {
	prefix := r.Config.MQTT.TopicPrefix + "/" + TopicSegment(node.Name)
	r.enqueue(outgoing{topic: prefix + "/state", payload: r.StateEvent(node, from, to, detail), retain: true})
	if !r.Config.IncidentProposals.Enabled {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.episodes[node.Name]; old != nil {
		old.timer.Stop()
		delete(r.episodes, node.Name)
	}
	if !r.failing(to) {
		return
	}
	sustained := time.Duration(r.Config.IncidentProposals.SustainedSeconds) * time.Second
	if r.sustainedOverride > 0 {
		sustained = r.sustainedOverride
	}
	ep := &episode{state: to}
	ep.timer = time.AfterFunc(sustained, func() {
		r.mu.Lock()
		current := r.episodes[node.Name]
		if current != ep {
			r.mu.Unlock()
			return // the node changed state in the meantime
		}
		delete(r.episodes, node.Name)
		r.mu.Unlock()
		payload := r.Proposal(node, to, clip(redact.Secrets(detail), 500), sustained)
		r.proposals.Add(1)
		r.enqueue(outgoing{topic: prefix + "/incident-proposal", payload: payload, retain: false})
	})
	r.episodes[node.Name] = ep
}

// Run sends what is queued until ctx ends, then stops the pending waits. Blocking.
func (r *EventReactor) Run(ctx context.Context) {
	defer func() {
		r.mu.Lock()
		for name, ep := range r.episodes {
			ep.timer.Stop()
			delete(r.episodes, name)
		}
		r.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-r.queue:
			if err := r.Publisher.Publish(ctx, m.topic, m.payload, m.retain); err != nil {
				r.failed.Add(1)
				r.printf("[node-healing] events: could not publish to %s: %v\n", m.topic, err)
				continue
			}
			r.published.Add(1)
		}
	}
}
