// HYDRA-UMC-NODE-HEALING - watchdog package: mqtt.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The smallest MQTT 3.1.1 publisher that does the job: connect, publish one message at QoS 0, disconnect.
// A watchdog publishes only when a node changes state, so a connection per message is cheap and there is
// no connection to keep alive, lose or reconnect. No new dependency: the packets are a few bytes each.
// TLS is optional, and a broker that is not on this machine is refused without it unless the
// configuration says on purpose that plain text is fine (see EventsConfig).
package watchdog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// maxMQTTPacket is the largest remaining length MQTT can express.
const maxMQTTPacket = 268435455

// Publisher sends one message to a broker.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte, retain bool) error
}

// MQTTTarget is one broker.
type MQTTTarget struct {
	// Broker is host:port.
	Broker   string
	TLS      bool
	CAFile   string
	ClientID string
	Username string
	Password string
	// Timeout bounds the connection and every read and write (5 seconds when zero).
	Timeout time.Duration
}

func mqttLength(n int) ([]byte, error) {
	if n < 0 || n > maxMQTTPacket {
		return nil, errors.New("an MQTT packet cannot be this long")
	}
	var out []byte
	for {
		digit := byte(n % 128)
		n /= 128
		if n > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
		if n == 0 {
			return out, nil
		}
	}
}

func mqttString(text string) ([]byte, error) {
	if len(text) > 65535 {
		return nil, errors.New("an MQTT string cannot be this long")
	}
	out := make([]byte, 2, 2+len(text))
	binary.BigEndian.PutUint16(out, uint16(len(text)))
	return append(out, text...), nil
}

func mqttConnectPacket(clientID, username, password string) ([]byte, error) {
	flags := byte(0x02) // clean session
	body := []byte{0x00, 0x04, 'M', 'Q', 'T', 'T', 4}
	payload, err := mqttString(clientID)
	if err != nil {
		return nil, err
	}
	if username != "" {
		flags |= 0x80
		user, userErr := mqttString(username)
		if userErr != nil {
			return nil, userErr
		}
		payload = append(payload, user...)
		if password != "" {
			flags |= 0x40
			pass, passErr := mqttString(password)
			if passErr != nil {
				return nil, passErr
			}
			payload = append(payload, pass...)
		}
	}
	body = append(body, flags, 0x00, 0x1e) // keep-alive 30 s
	body = append(body, payload...)
	length, err := mqttLength(len(body))
	if err != nil {
		return nil, err
	}
	return append(append([]byte{0x10}, length...), body...), nil
}

func mqttPublishPacket(topic string, payload []byte, retain bool) ([]byte, error) {
	if topic == "" || containsAny(topic, "#+\x00") {
		return nil, errors.New("a topic to publish to has no wildcards and is not empty")
	}
	name, err := mqttString(topic)
	if err != nil {
		return nil, err
	}
	body := append(name, payload...)
	length, err := mqttLength(len(body))
	if err != nil {
		return nil, err
	}
	header := byte(0x30)
	if retain {
		header |= 0x01
	}
	return append(append([]byte{header}, length...), body...), nil
}

func containsAny(text, set string) bool {
	for _, c := range text {
		for _, s := range set {
			if c == s {
				return true
			}
		}
	}
	return false
}

// Publish connects, sends the message at QoS 0 and disconnects.
func (t MQTTTarget) Publish(ctx context.Context, topic string, payload []byte, retain bool) error {
	packet, err := mqttPublishPacket(topic, payload, retain)
	if err != nil {
		return err
	}
	connect, err := mqttConnectPacket(t.ClientID, t.Username, t.Password)
	if err != nil {
		return err
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", t.Broker)
	if err != nil {
		return fmt.Errorf("cannot reach the broker at %s: %w", t.Broker, err)
	}
	conn := raw
	defer func() { _ = conn.Close() }()
	if t.TLS {
		host, _, splitErr := net.SplitHostPort(t.Broker)
		if splitErr != nil {
			return splitErr
		}
		config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if t.CAFile != "" {
			pem, readErr := os.ReadFile(t.CAFile)
			if readErr != nil {
				return fmt.Errorf("cannot read the CA file: %w", readErr)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return errors.New("the CA file holds no certificate")
			}
			config.RootCAs = pool
		}
		conn = tls.Client(raw, config)
	}
	if deadlineErr := conn.SetDeadline(time.Now().Add(timeout)); deadlineErr != nil {
		return deadlineErr
	}
	if _, writeErr := conn.Write(connect); writeErr != nil {
		return fmt.Errorf("MQTT exchange with %s failed: %w", t.Broker, writeErr)
	}
	reply := make([]byte, 4)
	read := 0
	for read < len(reply) {
		n, readErr := conn.Read(reply[read:])
		read += n
		if readErr != nil && read < len(reply) {
			return fmt.Errorf("the broker %s closed the connection before CONNACK: %w", t.Broker, readErr)
		}
	}
	if reply[0] != 0x20 || reply[1] != 0x02 {
		return errors.New("the broker did not answer CONNECT with CONNACK")
	}
	if reply[3] != 0 {
		return fmt.Errorf("the broker refused the connection (return code %d)", reply[3])
	}
	if _, writeErr := conn.Write(append(packet, 0xe0, 0x00)); writeErr != nil {
		return fmt.Errorf("MQTT exchange with %s failed: %w", t.Broker, writeErr)
	}
	return nil
}
