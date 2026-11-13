// HYDRA-UMC-NODE-HEALING - redact package: redact_edges_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Properties of the redaction that must hold whatever the text around a secret looks like: the secret is gone, harmless text is untouched
// and the structured forms (JSON values, lists of lines) are treated like the plain text.
package redact

import (
	"reflect"
	"strings"
	"testing"
)

func TestSecrets_TheSecretIsGoneWhateverSurroundsIt(t *testing.T) {
	cases := []struct{ name, text, secret string }{
		{"a url with a password", "connecting to postgres://app:hunter2hunter2@db.local:5432/main now", "hunter2hunter2"},
		{"a scheme with plus and dot", "see git+ssh.v2://deploy:s3cr3t-value-123@host/repo.git", "s3cr3t-value-123"},
		{"a bearer header", "Authorization: Bearer abcdef0123456789.token.value", "abcdef0123456789.token.value"},
		{"a key and value", "password=correct-horse-battery-staple", "correct-horse-battery-staple"},
		{"a quoted value", `api_key: "abc 123 def 456"`, "abc 123 def 456"},
		{"a json value", `{"client_secret": "zzzzzzzzzzzzzz"}`, "zzzzzzzzzzzzzz"},
		{"a flag", "tool --password swordfish-swordfish --verbose", "swordfish-swordfish"},
		{"a github token", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"an aws key", "key AKIAABCDEFGHIJKLMNOP in the log", "AKIAABCDEFGHIJKLMNOP"},
		{"a cookie", "Cookie: session=abcdef; theme=dark", "session=abcdef"},
	}
	for _, c := range cases {
		got := Secrets(c.text)
		if strings.Contains(got, c.secret) {
			t.Errorf("%s: the secret survived: %q", c.name, got)
		}
		if !strings.Contains(got, Marker) {
			t.Errorf("%s: no marker in %q", c.name, got)
		}
	}
}

func TestSecrets_HarmlessTextIsNotTouched(t *testing.T) {
	for _, text := range []string{
		"", "plain words", "a/b/c and http://example.org/path?x=1", "user@example.org wrote", "12:30:45 started", "ssh://host:22/path",
		"the word passphrase alone", "tokens are mentioned but nothing follows", "Basic usage of the tool",
	} {
		if got := Secrets(text); got != text {
			t.Errorf("Secrets(%q) = %q, want it unchanged", text, got)
		}
	}
}

func TestSecrets_IsIdempotent(t *testing.T) {
	for _, text := range []string{
		"password=abc123456", `{"token":"xyz"} and Bearer qwertyuiop`, "postgres://u:p@h/db", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----",
	} {
		once := Secrets(text)
		if twice := Secrets(once); twice != once {
			t.Errorf("not idempotent on %q: %q then %q", text, once, twice)
		}
	}
}

func TestSecrets_ACutPEMBlockHidesEverythingFromItsBeginMarker(t *testing.T) {
	got := Secrets("before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\nnever ends")
	if got != "before\n"+Marker {
		t.Errorf("got %q", got)
	}
	whole := Secrets("a -----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY----- b")
	if whole != "a "+Marker+" b" {
		t.Errorf("got %q", whole)
	}
}

func TestSecrets_ASchemeWithOddCharactersStillHidesItsPassword(t *testing.T) {
	got := Secrets("go to a+b://someone:abcdef@host/x")
	if strings.Contains(got, "abcdef") {
		t.Errorf("the credentials survived: %q", got)
	}
	if got := Secrets("mail me//someone@host"); got != "mail me//someone@host" {
		t.Errorf("a text with no password changed: %q", got)
	}
}

func TestJSON_StringsListsAndMapsAreRedactedAtEveryDepth(t *testing.T) {
	input := map[string]any{
		"name":     "node-a",
		"password": "plain-password-value",
		"pin":      12,
		"token":    float64(42),
		"count":    7,
		"enabled":  true,
		"secret":   true, // a boolean under a secret key is not a secret value
		"note":     "see Bearer abcdefghijklmnop for access",
		"nested":   map[string]any{"api_key": "k-123456789", "ok": "fine"},
		"list":     []any{"x", "password=hunter2hunter2", map[string]any{"credential": int64(5)}},
		"lines":    []string{"clean", "token=abcdef123456"},
		"nothing":  nil,
	}
	got, ok := JSON(input).(map[string]any)
	if !ok {
		t.Fatalf("JSON returned %T", JSON(input))
	}
	if got["password"] != Marker || got["token"] != Marker {
		t.Errorf("secret keys with string or number values must be hidden: %v", got)
	}
	if got["pin"] != 12 || got["count"] != 7 || got["enabled"] != true || got["secret"] != true || got["nothing"] != nil || got["name"] != "node-a" {
		t.Errorf("harmless values changed: %v", got)
	}
	if strings.Contains(got["note"].(string), "abcdefghijklmnop") {
		t.Errorf("the bearer token survived: %v", got["note"])
	}
	nested := got["nested"].(map[string]any)
	if nested["api_key"] != Marker || nested["ok"] != "fine" {
		t.Errorf("nested: %v", nested)
	}
	list := got["list"].([]any)
	if list[0] != "x" || strings.Contains(list[1].(string), "hunter2hunter2") || list[2].(map[string]any)["credential"] != Marker {
		t.Errorf("list: %v", list)
	}
	lines := got["lines"].([]any)
	if lines[0] != "clean" || strings.Contains(lines[1].(string), "abcdef123456") {
		t.Errorf("lines: %v", lines)
	}
	if input["password"] != "plain-password-value" {
		t.Error("the input must not be modified")
	}
}

func TestJSON_ScalarsAndUnknownTypesPassThrough(t *testing.T) {
	for _, value := range []any{nil, true, 3, 2.5, int64(9)} {
		if got := JSON(value); !reflect.DeepEqual(got, value) {
			t.Errorf("JSON(%v) = %v", value, got)
		}
	}
	if got := JSON("password=abcdef123456"); got == "password=abcdef123456" {
		t.Error("a plain string is redacted like text")
	}
	if got := JSON([]any{}); !reflect.DeepEqual(got, []any{}) {
		t.Errorf("an empty list stays an empty list: %#v", got)
	}
	if got := JSON([]string(nil)); !reflect.DeepEqual(got, []any{}) {
		t.Errorf("a nil list of strings becomes an empty list: %#v", got)
	}
}

func TestLines_ASecretAcrossLinesIsFoundAndBlankLinesSurvive(t *testing.T) {
	got := Lines([]string{"first", "-----BEGIN PRIVATE KEY-----", "AAAA", "-----END PRIVATE KEY-----", "last"})
	want := []string{"first", Marker, "last"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Lines(nil); got == nil || len(got) != 0 {
		t.Errorf("no lines must give an empty list, got %#v", got)
	}
	if got := Lines([]string{}); got == nil || len(got) != 0 {
		t.Errorf("an empty list must give an empty list, got %#v", got)
	}
	same := Lines([]string{"a", "", "b"})
	if !reflect.DeepEqual(same, []string{"a", "", "b"}) {
		t.Errorf("blank lines must be kept: %q", same)
	}
}

func TestIsSchemeCharAndIsIdent(t *testing.T) {
	for _, b := range []byte("+.-09azAZ") {
		if !isSchemeChar(b) {
			t.Errorf("%q is a scheme character", b)
		}
	}
	for _, b := range []byte(":/@ _!") {
		if isSchemeChar(b) {
			t.Errorf("%q is not a scheme character", b)
		}
	}
	for _, b := range []byte("_-09azAZ") {
		if !isIdent(b) {
			t.Errorf("%q is an identifier character", b)
		}
	}
	for _, b := range []byte(".+:/ @") {
		if isIdent(b) {
			t.Errorf("%q is not an identifier character", b)
		}
	}
}
