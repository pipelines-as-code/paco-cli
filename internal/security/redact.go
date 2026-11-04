package security

import (
	"regexp"
	"strings"
)

var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	// Scanning detects a JWT prefix; redaction consumes the full token.
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_.-]+`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.iam\.gserviceaccount\.com`),
}

const replacement = "[REDACTED]"

// Private-key markers get distinct placeholders so a later check can still
// tell where a key block started and ended.
const (
	privateKeyBeginPlaceholder = "[REDACTED PRIVATE KEY BEGIN]"
	privateKeyEndPlaceholder   = "[REDACTED PRIVATE KEY END]"
)

var (
	privateKeyBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	privateKeyEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
	pemBodyToken    = regexp.MustCompile(`^[A-Za-z0-9+/]{40,}={0,2}$`)
	pemTailToken    = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
	pemHeader       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*: `)
)

// Redact masks credential patterns in plain text. Key body lines are masked
// one line at a time without context, so a file and a diff of it (see
// RedactDiff) always mask the same lines and keep their line count.
func Redact(s string) string {
	return redact(s, false)
}

// RedactDiff is Redact for unified diffs: the one-character line prefix is
// set aside before masking, since base64 key lines may start with "+".
func RedactDiff(s string) string {
	return redact(s, true)
}

func redact(s string, diff bool) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		prefix := ""
		if diff && line != "" && strings.ContainsRune("+- ", rune(line[0])) {
			prefix, line = line[:1], line[1:]
		}
		if start, end, ok := maskedLine(line); ok {
			line = line[:start] + replacement + line[end:]
		}
		lines[i] = prefix + line
	}
	s = strings.Join(lines, "\n")
	s = privateKeyBegin.ReplaceAllString(s, privateKeyBeginPlaceholder)
	s = privateKeyEnd.ReplaceAllString(s, privateKeyEndPlaceholder)
	for _, re := range redactPatterns {
		s = re.ReplaceAllString(s, replacement)
	}
	return s
}

// unwrap returns the span of line left after removing surrounding
// whitespace, a "+" or "," after a closing quote (string concatenation or
// list item), quotes or backticks, and a trailing escaped newline. A bare
// trailing "+" is base64 and stays.
func unwrap(line string) (int, int) {
	start, end := 0, len(line)
	trim := func() {
		for start < end && strings.ContainsRune(" \t\r", rune(line[start])) {
			start++
		}
		for end > start && strings.ContainsRune(" \t\r", rune(line[end-1])) {
			end--
		}
	}
	trim()
	if end > start && (line[end-1] == '+' || line[end-1] == ',') {
		e := strings.TrimRight(line[start:end-1], " \t\r")
		if e != "" && strings.ContainsRune("\"'`", rune(e[len(e)-1])) {
			end = start + len(e)
		}
	}
	if end > start && strings.ContainsRune("\"'`", rune(line[end-1])) {
		end--
	}
	if end-start >= 2 && line[end-2:end] == `\n` {
		end -= 2
	}
	if end > start && strings.ContainsRune("\"'`", rune(line[start])) {
		start++
	}
	return start, end
}

// maskedLine reports the span of a line holding only a base64 key body token.
// Requiring upper case, lower case and digits keeps hex hashes and words out.
// A shorter final line must be a whole base64 quantum with padding, or be
// long enough with mixed character classes to rule out ordinary words.
func maskedLine(line string) (int, int, bool) {
	start, end := unwrap(line)
	token := line[start:end]
	mixed := strings.ContainsAny(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
		strings.ContainsAny(token, "abcdefghijklmnopqrstuvwxyz") &&
		strings.ContainsAny(token, "0123456789")
	body := pemBodyToken.MatchString(token) && mixed
	padded := strings.HasSuffix(token, "=")
	tail := pemTailToken.MatchString(token) && len(token)%4 == 0 &&
		(padded && len(token) >= 8 || mixed && len(token) >= 16)
	if !body && !tail {
		return 0, 0, false
	}
	return start, end, true
}

// PrivateKeysFullyRedacted reports whether every private key block in text,
// raw or already redacted, has only lines that Redact masks completely. A
// block must start with the only marker on its line, at the end of it, and
// close with the only marker on a line, at its start; in between, only blank
// lines, PEM headers and masked or maskable body lines are allowed. Keys
// written in other shapes, such as escaped newlines inside one JSON string,
// fail.
func PrivateKeysFullyRedacted(text string) bool {
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		begins, ends := markers(lines[i])
		if begins == 0 {
			continue
		}
		if begins > 1 || ends > 0 || !endsWithBegin(lines[i]) {
			return false
		}
		closed := false
		for i++; i < len(lines); i++ {
			line := lines[i]
			if begins, ends := markers(line); begins > 0 || ends > 0 {
				if begins > 0 || ends > 1 || !startsWithEnd(line) {
					return false
				}
				closed = true
				break
			}
			start, end := unwrap(line)
			payload := line[start:end]
			_, _, masked := maskedLine(line)
			if payload == "" || payload == replacement || masked || pemHeader.MatchString(payload) {
				continue
			}
			return false
		}
		if !closed {
			return false
		}
	}
	return true
}

// ContainsPrivateKey reports whether text has an unredacted BEGIN marker.
func ContainsPrivateKey(text string) bool {
	return privateKeyBegin.MatchString(text)
}

func markers(line string) (begins, ends int) {
	begins = len(privateKeyBegin.FindAllStringIndex(line, -1)) + strings.Count(line, privateKeyBeginPlaceholder)
	ends = len(privateKeyEnd.FindAllStringIndex(line, -1)) + strings.Count(line, privateKeyEndPlaceholder)
	return begins, ends
}

func endsWithBegin(line string) bool {
	start, end := unwrap(line)
	line = line[start:end]
	if strings.HasSuffix(line, privateKeyBeginPlaceholder) {
		return true
	}
	loc := privateKeyBegin.FindStringIndex(line)
	return loc != nil && loc[1] == len(line)
}

func startsWithEnd(line string) bool {
	line = strings.TrimLeft(line, " \t")
	line = strings.TrimLeft(line, "\"'`")
	if strings.HasPrefix(line, privateKeyEndPlaceholder) {
		return true
	}
	loc := privateKeyEnd.FindStringIndex(line)
	return loc != nil && loc[0] == 0
}

// Scrub replaces known credential literals before applying pattern redaction.
func Scrub(s string, literals ...string) string {
	return Redact(scrubLiterals(s, literals))
}

// ScrubDiff is Scrub for unified diffs.
func ScrubDiff(s string, literals ...string) string {
	return RedactDiff(scrubLiterals(s, literals))
}

func scrubLiterals(s string, literals []string) string {
	for _, lit := range literals {
		if lit != "" {
			s = strings.ReplaceAll(s, lit, replacement)
		}
	}
	return s
}
