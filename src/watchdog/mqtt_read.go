// HYDRA-UMC-NODE-HEALING - watchdog package: mqtt_read.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The smallest MQTT 3.1.1 reader that does the job: connect, subscribe to ONE topic at QoS 0, take the retained message the broker
// sends at once, disconnect. It never publishes. It exists so the watchdog can read the state of the mission queue that
// JOB-DISPATCHER keeps retained (see missionstate.go) without keeping a connection open.
package watchdog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// maxRetainedBytes bounds what a reader accepts as one message.
const maxRetainedBytes = 1 << 20

func mqttSubscribePacket(topic string) ([]byte, error) {
	if topic == "" || containsAny(topic, "#+\x00") {
		return nil, errors.New("a topic to read has no wildcards and is not empty")
	}
	name, err := mqttString(topic)
	if err != nil {
		return nil, err
	}
	body := append([]byte{0x00, 0x01}, name...) // packet id 1
	body = append(body, 0x00)                   // QoS 0
	length, err := mqttLength(len(body))
	if err != nil {
		return nil, err
	}
	return append(append([]byte{0x82}, length...), body...), nil
}

// mqttReadPacket reads one packet: its first byte and its body.
func mqttReadPacket(r io.Reader) (byte, []byte, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return 0, nil, err
	}
	remaining, multiplier := 0, 1
	for i := 0; ; i++ {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		remaining += int(b[0]&0x7f) * multiplier
		if b[0]&0x80 == 0 {
			break
		}
		multiplier *= 128
		if i >= 3 {
			return 0, nil, errors.New("a malformed MQTT length")
		}
	}
	if remaining > maxRetainedBytes {
		return 0, nil, errors.New("an MQTT message is larger than this reader accepts")
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return first[0], body, nil
}

// mqttPublishBody splits the body of a PUBLISH at QoS 0 into its topic and payload.
func mqttPublishBody(body []byte) (string, []byte, error) {
	if len(body) < 2 {
		return "", nil, errors.New("a PUBLISH that is too short")
	}
	n := int(body[0])<<8 | int(body[1])
	if len(body) < 2+n {
		return "", nil, errors.New("a PUBLISH whose topic is cut")
	}
	return string(body[2 : 2+n]), body[2+n:], nil
}

// ReadRetained returns the payload of the retained message on topic, waiting at most the timeout of the target for it. A broker that
// has none on that topic gives an error, never an empty answer taken for a state.
func (t MQTTTarget) ReadRetained(ctx context.Context, topic string) ([]byte, error) {
	subscribe, err := mqttSubscribePacket(topic)
	if err != nil {
		return nil, err
	}
	connect, err := mqttConnectPacket(t.ClientID+"-reader", t.Username, t.Password)
	if err != nil {
		return nil, err
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", t.Broker)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the broker at %s: %w", t.Broker, err)
	}
	conn := raw
	defer func() { _ = conn.Close() }()
	if t.TLS {
		host, _, splitErr := net.SplitHostPort(t.Broker)
		if splitErr != nil {
			return nil, splitErr
		}
		config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if t.CAFile != "" {
			pem, readErr := os.ReadFile(t.CAFile)
			if readErr != nil {
				return nil, fmt.Errorf("cannot read the CA file: %w", readErr)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("the CA file holds no certificate")
			}
			config.RootCAs = pool
		}
		conn = tls.Client(raw, config)
	}
	if deadlineErr := conn.SetDeadline(time.Now().Add(timeout)); deadlineErr != nil {
		return nil, deadlineErr
	}
	if _, writeErr := conn.Write(append(connect, subscribe...)); writeErr != nil {
		return nil, fmt.Errorf("MQTT exchange with %s failed: %w", t.Broker, writeErr)
	}
	for {
		first, body, readErr := mqttReadPacket(conn)
		if readErr != nil {
			return nil, fmt.Errorf("no retained message on %s: %w", topic, readErr)
		}
		switch first >> 4 {
		case 2: // CONNACK
			if len(body) != 2 || body[1] != 0 {
				return nil, errors.New("the broker refused the connection")
			}
		case 9: // SUBACK
			if len(body) < 3 || body[2] == 0x80 {
				return nil, errors.New("the broker refused the subscription")
			}
		case 3: // PUBLISH
			if first&0x06 != 0 {
				continue // only QoS 0 was asked for; anything else is not ours to read
			}
			got, payload, splitErr := mqttPublishBody(body)
			if splitErr != nil {
				return nil, splitErr
			}
			if got == topic {
				_, _ = conn.Write([]byte{0xe0, 0x00})
				return payload, nil
			}
		}
	}
}
