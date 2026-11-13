// HYDRA-UMC-NODE-HEALING - watchdog package: mqtt_edges_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Edge cases of the hand-written MQTT packets and of the two small clients (publisher and retained reader): the limits of the
// encoding, a broker that refuses, answers with something else, goes silent or hangs up, and a reader that must skip what is not
// its own. A scripted TCP broker plays each misbehaviour; nothing leaves the machine.
package watchdog

import (
	"bytes"
	"context"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scriptedBroker accepts ONE connection and hands it to the script; the connection is closed when the script returns.
func scriptedBroker(t *testing.T, script func(conn net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		script(conn)
	}()
	return listener.Addr().String()
}

// deadAddress is a local address where nothing listens any more.
func deadAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func decodeMQTTLength(t *testing.T, raw []byte) int {
	t.Helper()
	value, multiplier := 0, 1
	for i, b := range raw {
		value += int(b&0x7f) * multiplier
		multiplier *= 128
		if b&0x80 == 0 {
			if i != len(raw)-1 {
				t.Fatalf("bytes after the last length byte: %v", raw)
			}
			return value
		}
	}
	t.Fatalf("a length that never ends: %v", raw)
	return 0
}

func TestMQTTLength_BoundariesUseTheShortestForm(t *testing.T) {
	cases := map[int]int{0: 1, 127: 1, 128: 2, 16383: 2, 16384: 3, 2097151: 3, 2097152: 4, maxMQTTPacket: 4}
	for n, size := range cases {
		raw, err := mqttLength(n)
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if len(raw) != size {
			t.Errorf("%d took %d bytes, want %d", n, len(raw), size)
		}
		if got := decodeMQTTLength(t, raw); got != n {
			t.Errorf("%d decoded as %d", n, got)
		}
	}
}

func TestMQTTLength_RefusesWhatCannotBeExpressed(t *testing.T) {
	for _, n := range []int{-1, maxMQTTPacket + 1} {
		if _, err := mqttLength(n); err == nil {
			t.Errorf("%d must be refused", n)
		}
	}
}

func TestMQTTLength_RandomValuesRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(20260101))
	for i := 0; i < 300; i++ {
		n := rng.Intn(maxMQTTPacket + 1)
		raw, err := mqttLength(n)
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeMQTTLength(t, raw); got != n {
			t.Fatalf("%d decoded as %d", n, got)
		}
	}
}

func TestMQTTString_LimitsAndPrefix(t *testing.T) {
	out, err := mqttString("abc")
	if err != nil || !bytes.Equal(out, []byte{0, 3, 'a', 'b', 'c'}) {
		t.Fatalf("%v %v", out, err)
	}
	empty, err := mqttString("")
	if err != nil || !bytes.Equal(empty, []byte{0, 0}) {
		t.Fatalf("%v %v", empty, err)
	}
	if full, fullErr := mqttString(strings.Repeat("x", 65535)); fullErr != nil || len(full) != 65537 {
		t.Fatalf("the longest string must be accepted: %v", fullErr)
	}
	if _, err := mqttString(strings.Repeat("x", 65536)); err == nil {
		t.Error("a string of 65536 bytes must be refused")
	}
}

func TestMQTTConnectPacket_FlagsFollowTheCredentials(t *testing.T) {
	cases := []struct {
		user, pass string
		flags      byte
	}{
		{"", "", 0x02},
		{"", "secret", 0x02}, // a password with no user name is not sent
		{"ops", "", 0x82},
		{"ops", "secret", 0xc2},
	}
	for _, c := range cases {
		packet, err := mqttConnectPacket("client", c.user, c.pass)
		if err != nil {
			t.Fatal(err)
		}
		if packet[0] != 0x10 {
			t.Errorf("the first byte is %#x, want CONNECT", packet[0])
		}
		if got := packet[9]; got != c.flags {
			t.Errorf("user %q pass %q: flags %#x, want %#x", c.user, c.pass, got, c.flags)
		}
		if bytes.Contains(packet, []byte("secret")) != (c.flags == 0xc2) {
			t.Errorf("user %q pass %q: the password is on the wire only when it is used", c.user, c.pass)
		}
	}
}

func TestMQTTConnectPacket_RefusesFieldsThatAreTooLong(t *testing.T) {
	long := strings.Repeat("x", 65536)
	if _, err := mqttConnectPacket(long, "", ""); err == nil {
		t.Error("a client id that is too long must be refused")
	}
	if _, err := mqttConnectPacket("c", long, ""); err == nil {
		t.Error("a user name that is too long must be refused")
	}
	if _, err := mqttConnectPacket("c", "u", long); err == nil {
		t.Error("a password that is too long must be refused")
	}
}

func TestMQTTPublishPacket_RefusesTopicsThatAreNotPlain(t *testing.T) {
	for _, topic := range []string{"", "#", "a/+/b", "a/#", "a\x00b"} {
		if _, err := mqttPublishPacket(topic, []byte("x"), false); err == nil {
			t.Errorf("topic %q must be refused", topic)
		}
		if _, err := mqttSubscribePacket(topic); err == nil {
			t.Errorf("topic %q must be refused for a subscription", topic)
		}
	}
	if _, err := mqttPublishPacket(strings.Repeat("t", 65536), nil, false); err == nil {
		t.Error("a topic that is too long must be refused")
	}
	if _, err := mqttSubscribePacket(strings.Repeat("t", 65536)); err == nil {
		t.Error("a subscription topic that is too long must be refused")
	}
}

func TestMQTTPublishPacket_RoundTripsThroughTheReader(t *testing.T) {
	for _, retain := range []bool{false, true} {
		for _, payload := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte("y"), 300)} {
			packet, err := mqttPublishPacket("hydra/test/topic", payload, retain)
			if err != nil {
				t.Fatal(err)
			}
			first, body, err := mqttReadPacket(bytes.NewReader(packet))
			if err != nil {
				t.Fatal(err)
			}
			want := byte(0x30)
			if retain {
				want = 0x31
			}
			if first != want {
				t.Errorf("first byte %#x, want %#x", first, want)
			}
			topic, got, err := mqttPublishBody(body)
			if err != nil || topic != "hydra/test/topic" || !bytes.Equal(got, payload) {
				t.Errorf("%q %q %v", topic, got, err)
			}
		}
	}
}

func TestMQTTSubscribePacket_Layout(t *testing.T) {
	packet, err := mqttSubscribePacket("a/b")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x82, 8, 0, 1, 0, 3, 'a', '/', 'b', 0}
	if !bytes.Equal(packet, want) {
		t.Errorf("% x, want % x", packet, want)
	}
}

func TestMQTTPublishBody_RefusesCutDocuments(t *testing.T) {
	for _, body := range [][]byte{nil, {0}, {0, 5, 'a'}} {
		if _, _, err := mqttPublishBody(body); err == nil {
			t.Errorf("% x must be refused", body)
		}
	}
	topic, payload, err := mqttPublishBody([]byte{0, 1, 't'})
	if err != nil || topic != "t" || len(payload) != 0 {
		t.Errorf("%q %q %v", topic, payload, err)
	}
}

func TestMQTTReadPacket_RefusesBrokenInput(t *testing.T) {
	tooBig, _ := mqttLength(maxRetainedBytes + 1)
	cases := map[string][]byte{
		"nothing at all":            {},
		"a first byte and no size":  {0x30},
		"a size that never ends":    {0x30, 0x80, 0x80, 0x80, 0x80, 0x01},
		"a size over the reader":    append([]byte{0x30}, tooBig...),
		"a body that is cut":        {0x30, 0x05, 'a', 'b'},
		"a size cut in the middle":  {0x30, 0x80},
		"a size with 4 long digits": {0x30, 0xff, 0xff, 0xff, 0xff},
	}
	for name, raw := range cases {
		if _, _, err := mqttReadPacket(bytes.NewReader(raw)); err == nil {
			t.Errorf("%s must be an error", name)
		}
	}
	first, body, err := mqttReadPacket(bytes.NewReader([]byte{0xe0, 0x00}))
	if err != nil || first != 0xe0 || len(body) != 0 {
		t.Errorf("a packet with no body: %#x %v %v", first, body, err)
	}
}

func TestContainsAny(t *testing.T) {
	if !containsAny("a#b", "#+") || !containsAny("+", "#+") {
		t.Error("a wildcard must be found")
	}
	if containsAny("a/b", "#+") || containsAny("", "#+") {
		t.Error("a plain text has none")
	}
}

func TestMQTTTarget_PublishFailsLoudlyOnEveryBrokerMisbehaviour(t *testing.T) {
	cases := map[string]struct {
		script func(net.Conn)
		want   string
	}{
		"refused with a return code": {func(c net.Conn) {
			_, _, _ = mqttReadPacket(c)
			_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x05})
		}, "return code 5"},
		"something that is not a CONNACK": {func(c net.Conn) {
			_, _, _ = mqttReadPacket(c)
			_, _ = c.Write([]byte{0x30, 0x02, 0x00, 0x00})
		}, "did not answer CONNECT"},
		"hangs up before the answer": {func(c net.Conn) {
			_, _, _ = mqttReadPacket(c)
			_, _ = c.Write([]byte{0x20, 0x02})
		}, "closed the connection before CONNACK"},
		"hangs up at once": {func(c net.Conn) {}, "127.0.0.1"},
	}
	for name, c := range cases {
		addr := scriptedBroker(t, c.script)
		target := MQTTTarget{Broker: addr, ClientID: "t", Timeout: 2 * time.Second}
		err := target.Publish(context.Background(), "hydra/x", []byte("p"), false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestMQTTTarget_PublishSendsThePublishAndTheDisconnect(t *testing.T) {
	got := make(chan []byte, 1)
	addr := scriptedBroker(t, func(c net.Conn) {
		if _, _, err := mqttReadPacket(c); err != nil {
			return
		}
		_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
		first, body, err := mqttReadPacket(c)
		if err != nil {
			return
		}
		last, _, _ := mqttReadPacket(c)
		got <- append([]byte{first, last}, body...)
	})
	target := MQTTTarget{Broker: addr, ClientID: "t", Timeout: 2 * time.Second}
	if err := target.Publish(context.Background(), "hydra/x", []byte("p"), true); err != nil {
		t.Fatal(err)
	}
	select {
	case seen := <-got:
		if seen[0] != 0x31 || seen[1] != 0xe0 || !bytes.HasSuffix(seen, []byte("hydra/xp")) {
			t.Errorf("% x", seen)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the broker never saw the message")
	}
}

func TestMQTTTarget_PublishNeverReachesTheNetworkWithABadTopicOrAnUnreachableBroker(t *testing.T) {
	target := MQTTTarget{Broker: deadAddress(t), ClientID: "t", Timeout: time.Second}
	if err := target.Publish(context.Background(), "a/#", nil, false); err == nil || strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("a wildcard is refused before any connection: %v", err)
	}
	if err := target.Publish(context.Background(), "a/b", nil, false); err == nil || !strings.Contains(err.Error(), "cannot reach the broker") {
		t.Errorf("a dead broker: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := target.Publish(cancelled, "a/b", nil, false); err == nil {
		t.Error("a cancelled context must stop the publish")
	}
	tooLong := MQTTTarget{Broker: target.Broker, ClientID: strings.Repeat("c", 65536)}
	if err := tooLong.Publish(context.Background(), "a/b", nil, false); err == nil || strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("a client id that is too long is refused before any connection: %v", err)
	}
}

func TestMQTTTarget_ASilentBrokerIsGivenUpOnAtTheTimeout(t *testing.T) {
	addr := scriptedBroker(t, func(c net.Conn) { time.Sleep(700 * time.Millisecond) })
	target := MQTTTarget{Broker: addr, ClientID: "t", Timeout: 150 * time.Millisecond}
	started := time.Now()
	if err := target.Publish(context.Background(), "a/b", []byte("x"), false); err == nil {
		t.Fatal("a silent broker must be an error")
	}
	if elapsed := time.Since(started); elapsed > 600*time.Millisecond {
		t.Errorf("the publish waited %s, longer than its timeout", elapsed)
	}
}

func TestMQTTTarget_TLSRefusesAnUnusableCAFileAndAPlainTextBroker(t *testing.T) {
	dir := t.TempDir()
	noCert := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(noCert, []byte("this is not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ ca, want string }{
		"a CA file that is missing":     {filepath.Join(dir, "missing.pem"), "cannot read the CA file"},
		"a CA file with no certificate": {noCert, "holds no certificate"},
	}
	for name, c := range cases {
		addr := scriptedBroker(t, func(net.Conn) { time.Sleep(300 * time.Millisecond) })
		target := MQTTTarget{Broker: addr, TLS: true, CAFile: c.ca, ClientID: "t", Timeout: time.Second}
		if err := target.Publish(context.Background(), "a/b", nil, false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s (publish): %v", name, err)
		}
		addr = scriptedBroker(t, func(net.Conn) { time.Sleep(300 * time.Millisecond) })
		target.Broker = addr
		if _, err := target.ReadRetained(context.Background(), "a/b"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s (read): %v", name, err)
		}
	}
	// A broker that speaks plain MQTT to a client that insists on TLS must not be trusted with the message.
	addr := scriptedBroker(t, func(c net.Conn) {
		_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
		time.Sleep(200 * time.Millisecond)
	})
	target := MQTTTarget{Broker: addr, TLS: true, ClientID: "t", Timeout: time.Second}
	if err := target.Publish(context.Background(), "a/b", nil, false); err == nil {
		t.Error("TLS against a plain-text broker must fail")
	}
}

func TestMQTTTarget_ReadRetainedSkipsWhatIsNotItsOwnAndRefusesWhatIsBroken(t *testing.T) {
	connack := []byte{0x20, 0x02, 0x00, 0x00}
	suback := []byte{0x90, 0x03, 0x00, 0x01, 0x00}
	other, _ := mqttPublishPacket("hydra/other", []byte("not this"), true)
	wanted, _ := mqttPublishPacket("hydra/want", []byte("this one"), true)
	qos1 := []byte{0x32, 0x0e, 0x00, 0x0a, 'h', 'y', 'd', 'r', 'a', '/', 'w', 'a', 'n', 't', 0x00, 0x07}
	cases := map[string]struct {
		packets [][]byte
		want    string // "" means the payload "this one" is expected
	}{
		"a plain read":                          {[][]byte{connack, suback, wanted}, ""},
		"a message of another topic first":      {[][]byte{connack, suback, other, wanted}, ""},
		"a QoS 1 message is not ours":           {[][]byte{connack, suback, qos1, wanted}, ""},
		"an unknown packet type is ignored":     {[][]byte{connack, suback, {0xd0, 0x00}, wanted}, ""},
		"a refused connection":                  {[][]byte{{0x20, 0x02, 0x00, 0x05}}, "refused the connection"},
		"a CONNACK of the wrong size":           {[][]byte{{0x20, 0x01, 0x00}}, "refused the connection"},
		"a refused subscription":                {[][]byte{connack, {0x90, 0x03, 0x00, 0x01, 0x80}}, "refused the subscription"},
		"a SUBACK that is too short":            {[][]byte{connack, {0x90, 0x02, 0x00, 0x01}}, "refused the subscription"},
		"a PUBLISH that is too short":           {[][]byte{connack, suback, {0x30, 0x01, 0x00}}, "too short"},
		"a PUBLISH whose topic is cut":          {[][]byte{connack, suback, {0x30, 0x03, 0x00, 0x09, 'a'}}, "topic is cut"},
		"hanging up after the subscription":     {[][]byte{connack, suback}, "no retained message"},
		"a message that is larger than allowed": {[][]byte{connack, suback, append([]byte{0x30, 0x80, 0x80, 0x41}, 'x')}, "larger than this reader accepts"},
	}
	for name, c := range cases {
		packets := c.packets
		addr := scriptedBroker(t, func(conn net.Conn) {
			if _, _, err := mqttReadPacket(conn); err != nil {
				return
			}
			for _, packet := range packets {
				_, _ = conn.Write(packet)
			}
			time.Sleep(100 * time.Millisecond)
		})
		target := MQTTTarget{Broker: addr, ClientID: "t", Timeout: time.Second}
		payload, err := target.ReadRetained(context.Background(), "hydra/want")
		if c.want == "" {
			if err != nil || string(payload) != "this one" {
				t.Errorf("%s: %q %v", name, payload, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q %v, want %q", name, payload, err, c.want)
		}
	}
}

func TestMQTTTarget_ReadRetainedTellsItselfApartFromThePublisher(t *testing.T) {
	seen := make(chan string, 1)
	addr := scriptedBroker(t, func(conn net.Conn) {
		_, body, err := mqttReadPacket(conn)
		if err != nil {
			return
		}
		seen <- string(body)
		_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00, 0x90, 0x03, 0x00, 0x01, 0x00})
		packet, _ := mqttPublishPacket("hydra/want", nil, true)
		_, _ = conn.Write(packet)
		time.Sleep(100 * time.Millisecond)
	})
	target := MQTTTarget{Broker: addr, ClientID: "node-healing", Timeout: time.Second}
	payload, err := target.ReadRetained(context.Background(), "hydra/want")
	if err != nil || len(payload) != 0 {
		t.Fatalf("an empty retained payload is returned as it is: %q %v", payload, err)
	}
	if body := <-seen; !strings.Contains(body, "node-healing-reader") {
		t.Errorf("the reader must not take the client id of the publisher: %q", body)
	}
	if _, err := ParseMissionState(payload); err == nil {
		t.Error("an empty document must never be taken for a state")
	}
}

func TestMQTTTarget_ReadRetainedFailsFastWhenTheBrokerCannotBeReached(t *testing.T) {
	target := MQTTTarget{Broker: deadAddress(t), ClientID: "t", Timeout: time.Second}
	if _, err := target.ReadRetained(context.Background(), "a/b"); err == nil || !strings.Contains(err.Error(), "cannot reach the broker") {
		t.Errorf("%v", err)
	}
	long := MQTTTarget{Broker: target.Broker, ClientID: strings.Repeat("c", 65536)}
	if _, err := long.ReadRetained(context.Background(), "a/b"); err == nil || strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("a client id that is too long is refused before any connection: %v", err)
	}
}
