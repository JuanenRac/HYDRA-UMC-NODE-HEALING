// =============================================================================
// HYDRA-UMC-NODE-HEALING - src/redact/redact.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
// =============================================================================

// Package redact hides well-known secret shapes in text and JSON-like values. It is a port of the
// shared module of the SDK (tools/canonical/redaction.py); the cases every implementation has to
// pass are in testdata/vectors.json, a copy of contracts/redaction/vectors.json of the SDK.
//
// A fixed list of real secret shapes, never a "looks random enough" heuristic. Go's regexp has no
// look-behind, look-ahead or back-references, so those three are done by hand (see lookbehind
// below); the one visible difference is that the length of a password inside a URL is capped at 1000
// characters instead of 1024 (the limit of Go's regexp repetition).
package redact

import (
	"regexp"
	"strings"
)

// Marker replaces every secret found.
const Marker = "[REDACTED]"

var secretKeyNames = []string{
	"password", "passwd", "pwd",
	"secret", "token", "api_key", "apikey", "api-key",
	"private_key", "privatekey", "private-key",
	"credential", "credentials",
	"ssh_key", "sshkey",
}

var keys = strings.Join(secretKeyNames, "|")

const identRun = `[A-Za-z0-9_-]`

var (
	keyValueRe     = regexp.MustCompile(`(?i)(` + identRun + `*(?:` + keys + `)` + identRun + `*\s*[:=]\s*)(?:"[^\r\n]*?"|'[^\r\n]*?'|\S+)`)
	jsonKeyValueRe = regexp.MustCompile(`(?i)("` + identRun + `*(?:` + keys + `)` + identRun + `*"\s*:\s*)(?:"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?|true|false|null)`)
	bearerRe       = regexp.MustCompile(`(?i)\bBearer\s+\S+`)
	urlCredsRe     = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]{0,39}://)([^\s:/@]{1,256}):([^\s@]{1,1000})@`)
	pemBlockRe     = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	pemBeginRe     = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	basicAuthRe    = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization\s*[:=]\s*)basic\s+[A-Za-z0-9+/=]{6,}`)
	flagValueRe    = regexp.MustCompile(`(?i)(--?` + identRun + `*(?:` + keys + `)s?` + identRun + `*)([ \t]+)([^\s-]\S*)`)
	basicBareRe    = regexp.MustCompile(`\bBasic\s+([A-Za-z0-9+/=_-]{8,})`)
	cookieRe       = regexp.MustCompile(`(?i)(\b(?:Set-)?Cookie\s*:\s*)[^\r\n]+`)
	urlTokenRe     = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]{0,39}://)([^\s:/@]{16,1000})@`)
	secretKeyRe    = regexp.MustCompile(`(?i)"?` + identRun + `*(?:` + keys + `)` + identRun + `*"?\s*[:=]\s*`)
	secretKeyFull  = regexp.MustCompile(`(?i)^"?` + identRun + `*(?:` + keys + `)` + identRun + `*"?\s*[:=]\s*$`)
	skShapeRe      = regexp.MustCompile(`\bsk-(?:ant-|proj-)?([A-Za-z0-9_-]{20,})`)
	tokenShapes    = []*regexp.Regexp{
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	}
)

func isIdent(b byte) bool {
	return b == '_' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isSchemeChar(b byte) bool {
	return b == '+' || b == '.' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// replaceAll replaces every match of re that is not preceded by a byte for which forbidden is true
// (a negative look-behind). A match that is refused is retried one byte further on, as a regular
// expression engine with look-behind would.
func replaceAll(re *regexp.Regexp, s string, forbidden func(byte) bool, repl func(m []int) string) string {
	var out strings.Builder
	last, pos := 0, 0
	for pos <= len(s) {
		loc := re.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}
		start := loc[0]
		if start > 0 && forbidden != nil && forbidden(s[start-1]) {
			pos = start + 1
			continue
		}
		out.WriteString(s[last:start])
		out.WriteString(repl(loc))
		last = loc[1]
		if loc[1] == loc[0] {
			pos = loc[1] + 1
		} else {
			pos = loc[1]
		}
	}
	out.WriteString(s[last:])
	return out.String()
}

func group(s string, m []int, n int) string {
	if m[2*n] < 0 {
		return ""
	}
	return s[m[2*n]:m[2*n+1]]
}

func hasAny(s, set string) bool { return strings.ContainsAny(s, set) }

func redactNested(s string) string {
	var pieces strings.Builder
	cursor, pos := 0, 0
	for pos <= len(s) {
		loc := secretKeyRe.FindStringIndex(s[pos:])
		if loc == nil {
			break
		}
		start, end := loc[0]+pos, loc[1]+pos
		if start > 0 && isIdent(s[start-1]) {
			pos = start + 1
			continue
		}
		pos = end
		if end == start {
			pos = end + 1
		}
		if start < cursor {
			continue
		}
		if end >= len(s) || (s[end] != '{' && s[end] != '[') {
			continue
		}
		opening := s[end]
		closing := byte('}')
		if opening == '[' {
			closing = ']'
		}
		depth, inString, escape, stop := 0, false, false, -1
		for i := end; i < len(s); i++ {
			ch := s[i]
			if inString {
				if escape {
					escape = false
				} else if ch == '\\' {
					escape = true
				} else if ch == '"' {
					inString = false
				}
				continue
			}
			if ch == '"' {
				inString = true
			} else if ch == opening {
				depth++
			} else if ch == closing {
				depth--
				if depth == 0 {
					stop = i + 1
					break
				}
			}
		}
		if stop < 0 {
			continue
		}
		pieces.WriteString(s[cursor:start])
		pieces.WriteString(s[start:end] + Marker)
		cursor = stop
		if stop > pos {
			pos = stop
		}
	}
	pieces.WriteString(s[cursor:])
	return pieces.String()
}

// Secrets returns text with every recognised secret shape replaced by Marker.
func Secrets(text string) string {
	if text == "" {
		return text
	}
	result := text
	// a PEM block cut off before its END marker hides everything from its BEGIN marker on
	for _, m := range pemBeginRe.FindAllStringIndex(result, -1) {
		if !strings.Contains(result[m[1]:], "-----END") {
			result = result[:m[0]] + Marker
			break
		}
	}
	result = pemBlockRe.ReplaceAllString(result, Marker)
	result = bearerRe.ReplaceAllString(result, "Bearer "+Marker)
	result = basicAuthRe.ReplaceAllString(result, "${1}Basic "+Marker)
	result = replaceAllWithoutLookbehind(basicBareRe, result, func(m []int) string {
		if hasAny(group(result, m, 1), "0123456789+/=") {
			return "Basic " + Marker
		}
		return result[m[0]:m[1]]
	})
	result = cookieRe.ReplaceAllString(result, "${1}"+Marker)
	snapshot := result
	result = replaceAllWithoutLookbehind(skShapeRe, snapshot, func(m []int) string {
		if hasAny(group(snapshot, m, 1), "0123456789") {
			return Marker
		}
		return snapshot[m[0]:m[1]]
	})
	for _, shape := range tokenShapes {
		result = shape.ReplaceAllString(result, Marker)
	}
	userinfo := result
	result = replaceAll(urlTokenRe, userinfo, isSchemeChar, func(m []int) string { return group(userinfo, m, 1) + Marker + "@" })
	creds := result
	result = replaceAll(urlCredsRe, creds, isSchemeChar, func(m []int) string {
		return group(creds, m, 1) + group(creds, m, 2) + ":" + Marker + "@"
	})
	result = redactNested(result)
	result = jsonKeyValueRe.ReplaceAllString(result, "${1}"+Marker)
	kv := result
	result = replaceAll(keyValueRe, kv, isIdent, func(m []int) string { return group(kv, m, 1) + Marker })
	flags := result
	result = replaceAll(flagValueRe, flags, isIdent, func(m []int) string { return group(flags, m, 1) + group(flags, m, 2) + Marker })
	return result
}

// replaceAllWithoutLookbehind is replaceAll for patterns that start with \b (they must see the text
// before the match, so the whole string is searched once with FindAll).
func replaceAllWithoutLookbehind(re *regexp.Regexp, s string, repl func(m []int) string) string {
	var out strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		out.WriteString(s[last:m[0]])
		out.WriteString(repl(m))
		last = m[1]
	}
	out.WriteString(s[last:])
	return out.String()
}

// JSON applies Secrets to every string inside a JSON-like value (maps, slices, strings), and hides
// the value of any key whose name is a secret word when that value is a string or a number.
func JSON(value any) any {
	switch v := value.(type) {
	case string:
		return Secrets(v)
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Secrets(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = JSON(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			switch item.(type) {
			case string, float64, int, int64:
				if secretKeyFull.MatchString(key + ":") {
					out[key] = Marker
					continue
				}
			}
			out[key] = JSON(item)
		}
		return out
	default:
		return value
	}
}

// Lines applies Secrets across the whole joined text and splits it back, so a secret that spans
// several entries (a PEM block cut over two lines) is still recognised.
func Lines(lines []string) []string {
	if len(lines) == 0 {
		return []string{}
	}
	return strings.Split(Secrets(strings.Join(lines, "\n")), "\n")
}
