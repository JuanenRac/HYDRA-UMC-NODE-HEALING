// HYDRA-UMC-NODE-HEALING - watchdog package: events_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE

package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type published struct {
	topic   string
	payload []byte
	retain  bool
}

// fakeBroker is a real TCP listener that speaks just enough MQTT 3.1.1: it answers CONNECT with CONNACK and records PUBLISH.
type fakeBroker struct {
	listener net.Listener
	mu       sync.Mutex
	got      []published
	connects int
	user     string
	pass     string
}

func newFakeBroker(t *testing.T) *fakeBroker {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{listener: l}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}
			go b.serve(conn)
		}
	}()
	return b
}

func readPacket(conn net.Conn) (byte, []byte, error) {
	head := make([]byte, 1)
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, nil, err
	}
	length, mult := 0, 1
	for {
		one := make([]byte, 1)
		if _, err := io.ReadFull(conn, one); err != nil {
			return 0, nil, err
		}
		length += int(one[0]&0x7f) * mult
		if one[0]&0x80 == 0 {
			break
		}
		mult *= 128
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return 0, nil, err
	}
	return head[0], body, nil
}

func mqttText(body []byte) (string, []byte) {
	n := int(body[0])<<8 | int(body[1])
	return string(body[2 : 2+n]), body[2+n:]
}

func (b *fakeBroker) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header, body, err := readPacket(conn)
	if err != nil || header != 0x10 {
		return
	}
	// protocol name, level, flags, keep-alive, then the client id (and user and password when the flags say so)
	flags := body[7]
	rest := body[10:]
	_, rest = mqttText(rest)
	b.mu.Lock()
	b.connects++
	if flags&0x80 != 0 {
		b.user, rest = mqttText(rest)
	}
	if flags&0x40 != 0 {
		b.pass, _ = mqttText(rest)
	}
	b.mu.Unlock()
	if _, err := conn.Write([]byte{0x20, 0x02, 0x00, 0x00}); err != nil {
		return
	}
	header, body, err = readPacket(conn)
	if err != nil || header&0xf0 != 0x30 {
		return
	}
	topic, payload := mqttText(body)
	b.mu.Lock()
	b.got = append(b.got, published{topic, append([]byte(nil), payload...), header&0x01 != 0})
	b.mu.Unlock()
}

func (b *fakeBroker) messages() []published {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]published(nil), b.got...)
}

func (b *fakeBroker) waitFor(t *testing.T, count int) []published {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := b.messages(); len(got) >= count {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d message(s), got %d", count, len(b.messages()))
	return nil
}

func enabledConfig(broker string) EventsConfig {
	cfg := DefaultEventsConfig()
	cfg.MQTT.Enabled = true
	cfg.MQTT.Broker = broker
	return cfg
}

func runReactor(t *testing.T, r *EventReactor) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestEventsConfigIsOffByDefault(t *testing.T) {
	cfg := DefaultEventsConfig()
	if cfg.MQTT.Enabled || cfg.IncidentProposals.Enabled {
		t.Fatal("nothing may be on in the default configuration")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEventsConfigLoadAndRefusals(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.json", `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883"},"incidentProposals":{"enabled":true,"sustainedSeconds":30}}`)
	cfg, err := LoadEventsConfig(good)
	if err != nil || cfg.MQTT.TopicPrefix != "hydra/health" || cfg.IncidentProposals.SustainedSeconds != 30 {
		t.Fatalf("good file: %+v %v", cfg, err)
	}
	bad := map[string]string{
		"unknown key":          `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883","surprise":1}}`,
		"no broker":            `{"mqtt":{"enabled":true}}`,
		"plain text off box":   `{"mqtt":{"enabled":true,"broker":"10.0.0.5:1883"}}`,
		"ca without tls":       `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883","caFile":"x.pem"}}`,
		"password no user":     `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883","passwordFile":"x"}}`,
		"wildcard prefix":      `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883","topicPrefix":"hydra/#"}}`,
		"queue too big":        `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883","queueSize":100000}}`,
		"proposals without mq": `{"incidentProposals":{"enabled":true}}`,
		"proposal time":        `{"mqtt":{"enabled":true,"broker":"127.0.0.1:1883"},"incidentProposals":{"enabled":true,"sustainedSeconds":0}}`,
		"not json":             `nope`,
	}
	for name, text := range bad {
		if _, loadErr := LoadEventsConfig(write(strings.ReplaceAll(name, " ", "_")+".json", text)); loadErr == nil {
			t.Errorf("%s: the file must be refused", name)
		}
	}
	// a broker off this machine is accepted with TLS, or on purpose without it
	for _, text := range []string{`{"mqtt":{"enabled":true,"broker":"broker.lan:8883","tls":true}}`, `{"mqtt":{"enabled":true,"broker":"10.0.0.5:1883","allowPlaintext":true}}`} {
		if _, loadErr := LoadEventsConfig(write("ok.json", text)); loadErr != nil {
			t.Errorf("%s: %v", text, loadErr)
		}
	}
	if _, loadErr := LoadEventsConfig(filepath.Join(dir, "missing.json")); loadErr == nil {
		t.Error("a missing file must be an error")
	}
}

func TestTopicSegmentAndLabel(t *testing.T) {
	if got := TopicSegment("a/b#c+d e"); got != "a_b_c_d_e" {
		t.Fatalf("got %q", got)
	}
	if TopicSegment("") != "_" {
		t.Fatal("empty name")
	}
	if got := label(strings.Repeat("x", 100)); len(got) != 64 {
		t.Fatalf("label length %d", len(got))
	}
}

func TestMQTTTargetPublishesToARealBroker(t *testing.T) {
	broker := newFakeBroker(t)
	target := MQTTTarget{Broker: broker.listener.Addr().String(), ClientID: "test-client", Username: "healer", Password: "s3cret-pass", Timeout: 2 * time.Second}
	if err := target.Publish(context.Background(), "hydra/health/n1/state", []byte(`{"a":1}`), true); err != nil {
		t.Fatal(err)
	}
	got := broker.waitFor(t, 1)[0]
	if got.topic != "hydra/health/n1/state" || string(got.payload) != `{"a":1}` || !got.retain {
		t.Fatalf("got %+v", got)
	}
	if broker.user != "healer" || broker.pass != "s3cret-pass" {
		t.Fatal("the credentials were not sent")
	}
	if err := target.Publish(context.Background(), "bad/#", nil, false); err == nil {
		t.Fatal("a wildcard topic must be refused")
	}
	dead := MQTTTarget{Broker: "127.0.0.1:1", Timeout: 500 * time.Millisecond}
	if err := dead.Publish(context.Background(), "t", nil, false); err == nil {
		t.Fatal("an unreachable broker must be an error")
	}
}

func TestEveryTransitionIsPublishedAsAnEventEnvelope(t *testing.T) {
	broker := newFakeBroker(t)
	cfg := enabledConfig(broker.listener.Addr().String())
	r := NewEventReactor(cfg, MQTTTarget{Broker: cfg.MQTT.Broker, ClientID: "c", Timeout: time.Second}, func(string, ...any) {})
	runReactor(t, r)
	node := Node{Name: "job-dispatcher"}
	r.OnTransition(node, StatusUnknown, StatusHealthy, "")
	r.OnTransition(node, StatusHealthy, StatusUnreachable, "dial failed: password=hunter2 refused")
	got := broker.waitFor(t, 2)
	// the worker takes the queue in order, but each message has its own connection, so match by content
	byTo := map[string]map[string]any{}
	for _, m := range got {
		if m.topic != "hydra/health/job-dispatcher/state" || !m.retain {
			t.Fatalf("topic or retain wrong: %+v", m)
		}
		var event map[string]any
		if err := json.Unmarshal(m.payload, &event); err != nil {
			t.Fatal(err)
		}
		byTo[event["to"].(string)] = event
	}
	for _, event := range byTo {
		if event["schema_version"] != "1.0" || event["type"] != EventTransition || event["source"] != EventSource || event["node"] != "job-dispatcher" {
			t.Fatalf("envelope wrong: %v", event)
		}
		if id, _ := event["event_id"].(string); id == "" {
			t.Fatal("event_id missing")
		}
		if _, err := time.Parse(time.RFC3339Nano, event["timestamp_utc"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	down := byTo["UNREACHABLE"]
	if down["from"] != "HEALTHY" || strings.Contains(down["detail"].(string), "hunter2") {
		t.Fatalf("the detail must be redacted: %v", down)
	}
	seqs := []float64{byTo["HEALTHY"]["sequence"].(float64), down["sequence"].(float64)}
	if seqs[0] != 0 || seqs[1] != 1 {
		t.Fatalf("sequences %v", seqs)
	}
	if r.Published() != 2 || r.Failed() != 0 || r.Dropped() != 0 {
		t.Fatalf("counters %d %d %d", r.Published(), r.Failed(), r.Dropped())
	}
}

type failingPublisher struct{}

func (failingPublisher) Publish(context.Context, string, []byte, bool) error {
	return errors.New("broker down")
}

func TestAFailedPublishIsCountedAndNeverBlocks(t *testing.T) {
	cfg := enabledConfig("127.0.0.1:1883")
	r := NewEventReactor(cfg, failingPublisher{}, func(string, ...any) {})
	runReactor(t, r)
	r.OnTransition(Node{Name: "n"}, StatusUnknown, StatusHealthy, "")
	deadline := time.Now().Add(2 * time.Second)
	for r.Failed() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.Failed() != 1 || r.Published() != 0 {
		t.Fatalf("failed %d published %d", r.Failed(), r.Published())
	}
}

func TestAFullQueueDropsInsteadOfBlocking(t *testing.T) {
	cfg := enabledConfig("127.0.0.1:1883")
	cfg.MQTT.QueueSize = 1
	r := NewEventReactor(cfg, failingPublisher{}, nil) // no worker running: nothing empties the queue
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			r.OnTransition(Node{Name: "n"}, StatusUnknown, StatusHealthy, "")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTransition blocked on a full queue")
	}
	if r.Dropped() != 4 {
		t.Fatalf("dropped %d, want 4", r.Dropped())
	}
}

func proposalConfig(broker string) EventsConfig {
	cfg := enabledConfig(broker)
	cfg.IncidentProposals.Enabled = true
	cfg.IncidentProposals.SustainedSeconds = 1
	return cfg
}

var labelRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func TestASustainedFailureProposesOneIncident(t *testing.T) {
	broker := newFakeBroker(t)
	cfg := proposalConfig(broker.listener.Addr().String())
	r := NewEventReactor(cfg, MQTTTarget{Broker: cfg.MQTT.Broker, ClientID: "c", Timeout: time.Second}, func(string, ...any) {})
	r.sustainedOverride = 50 * time.Millisecond
	runReactor(t, r)
	node := Node{Name: "orchestrator/main"}
	r.OnTransition(node, StatusHealthy, StatusUnreachable, "connection refused to http://x/?token=abc123def456")
	got := broker.waitFor(t, 2)
	var proposal map[string]any
	for _, m := range got {
		if strings.HasSuffix(m.topic, "/incident-proposal") {
			if m.topic != "hydra/health/orchestrator_main/incident-proposal" || m.retain {
				t.Fatalf("proposal topic or retain wrong: %+v", m)
			}
			if err := json.Unmarshal(m.payload, &proposal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if proposal == nil {
		t.Fatal("no proposal arrived")
	}
	// the contract is closed: exactly these keys, with these shapes
	allowed := map[string]bool{"schema_version": true, "incidentId": true, "sourceNode": true, "detectedAt": true, "severity": true, "component": true, "symptom": true, "evidenceRefs": true, "redactionLevel": true, "requestedBy": true, "correlationId": true}
	for k := range proposal {
		if !allowed[k] {
			t.Errorf("key %q is not in the contract", k)
		}
	}
	for k := range allowed {
		if _, ok := proposal[k]; !ok && k != "schema_version" {
			t.Errorf("key %q is missing", k)
		}
	}
	for _, k := range []string{"incidentId", "sourceNode", "correlationId"} {
		if !labelRe.MatchString(proposal[k].(string)) {
			t.Errorf("%s %q does not match the pattern", k, proposal[k])
		}
	}
	if proposal["severity"] != "critical" || proposal["redactionLevel"] != "sanitized" || proposal["requestedBy"] != nil || proposal["schema_version"] != "1.0" {
		t.Fatalf("fields wrong: %v", proposal)
	}
	if _, err := time.Parse(time.RFC3339, proposal["detectedAt"].(string)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(proposal["symptom"].(string), "abc123def456") || !strings.Contains(proposal["symptom"].(string), "UNREACHABLE") {
		t.Fatalf("symptom: %v", proposal["symptom"])
	}
	refs := proposal["evidenceRefs"].([]any)
	if len(refs) != 1 || refs[0] != "mqtt:hydra/health/orchestrator_main/state" {
		t.Fatalf("refs %v", refs)
	}
	time.Sleep(200 * time.Millisecond)
	if r.Proposals() != 1 {
		t.Fatalf("one proposal per episode, got %d", r.Proposals())
	}
}

func TestANodeThatComesBackInTimeProposesNothing(t *testing.T) {
	broker := newFakeBroker(t)
	cfg := proposalConfig(broker.listener.Addr().String())
	r := NewEventReactor(cfg, MQTTTarget{Broker: cfg.MQTT.Broker, ClientID: "c", Timeout: time.Second}, func(string, ...any) {})
	r.sustainedOverride = 150 * time.Millisecond
	runReactor(t, r)
	node := Node{Name: "n"}
	r.OnTransition(node, StatusHealthy, StatusUnhealthy, "")
	r.OnTransition(node, StatusUnhealthy, StatusHealthy, "")
	time.Sleep(400 * time.Millisecond)
	for _, m := range broker.waitFor(t, 2) {
		if strings.HasSuffix(m.topic, "/incident-proposal") {
			t.Fatal("a node that recovered must not be proposed")
		}
	}
	if r.Proposals() != 0 {
		t.Fatal("no proposal expected")
	}
}

func TestOnlyFailingStatesAreProposed(t *testing.T) {
	cfg := proposalConfig("127.0.0.1:1883")
	r := NewEventReactor(cfg, failingPublisher{}, nil)
	for _, s := range []Status{StatusHealthy, StatusDegraded, StatusMisconfigured, StatusUnknown} {
		if r.failing(s) {
			t.Errorf("%s must not be proposed by default", s)
		}
	}
	for _, s := range []Status{StatusUnreachable, StatusInvalid, StatusUnhealthy} {
		if !r.failing(s) {
			t.Errorf("%s must be proposed", s)
		}
	}
	r.Config.IncidentProposals.IncludeDegraded = true
	if !r.failing(StatusDegraded) {
		t.Error("degraded is proposed when the file says so")
	}
	if got := string(r.Proposal(Node{Name: "n"}, StatusUnhealthy, "", time.Minute)); !strings.Contains(got, `"severity":"warning"`) {
		t.Errorf("unhealthy is a warning: %s", got)
	}
}

type seenReactor struct{ seen []string }

func (r *seenReactor) OnTransition(n Node, _, to Status, _ string) {
	r.seen = append(r.seen, n.Name+":"+to.String())
}

func TestMultiReactorCallsEveryReactorAndSkipsNil(t *testing.T) {
	a, b := &seenReactor{}, &seenReactor{}
	MultiReactor{a, nil, b}.OnTransition(Node{Name: "x"}, StatusUnknown, StatusHealthy, "")
	if len(a.seen) != 1 || len(b.seen) != 1 {
		t.Fatalf("%v %v", a.seen, b.seen)
	}
}

func TestStatusEndpointShowsTheEventCounters(t *testing.T) {
	cfg := enabledConfig("127.0.0.1:1883")
	r := NewEventReactor(cfg, failingPublisher{}, nil)
	r.dropped.Store(3)
	wd := NewWatchdog(nil, LogReactor{})
	server := &StatusServer{Watchdog: wd, Version: "test", Started: time.Now(), Events: r}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "hydra_node_healing_events_dropped_total 3") || !strings.Contains(body, "hydra_node_healing_incident_proposals_total 0") {
		t.Fatalf("metrics: %s", body)
	}
	plain := &StatusServer{Watchdog: wd, Version: "test", Started: time.Now()}
	rec = httptest.NewRecorder()
	plain.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(rec.Body.String(), "events_published") {
		t.Fatal("without events the metrics stay as they were")
	}
}

func TestClipKeepsCharactersWhole(t *testing.T) {
	if got := clip("ñññññ", 3); got != "ñ" {
		t.Fatalf("got %q", got)
	}
	if got := clip("short", 10); got != "short" {
		t.Fatalf("got %q", got)
	}
}
