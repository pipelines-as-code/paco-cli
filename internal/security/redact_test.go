package security

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "GitHub token ghp",
			input: "token is ghp_ABCDEFghijklmnopqrstuvwx here",
			want:  "token is [REDACTED] here",
		},
		{
			name:  "GitHub PAT",
			input: "github_pat_ABCDEFGHIJ1234567890_morestuff",
			want:  "[REDACTED]",
		},
		{
			name:  "JWT",
			input: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0",
			want:  "[REDACTED]",
		},
		{
			name:  "AWS key",
			input: "AKIAIOSFODNN7EXAMPLE is the key",
			want:  "[REDACTED] is the key",
		},
		{
			name:  "Anthropic API key",
			input: "key sk-ant-api03-ABCDEFGHIJ_klmnopqrst-uvw end",
			want:  "key [REDACTED] end",
		},
		{
			name:  "PEM header",
			input: "-----BEGIN RSA PRIVATE KEY-----",
			want:  "[REDACTED PRIVATE KEY BEGIN]",
		},
		{
			name:  "GCP service account",
			input: "my-sa@my-project.iam.gserviceaccount.com",
			want:  "[REDACTED]",
		},
		{
			name:  "no secrets",
			input: "this is clean text with no credentials",
			want:  "this is clean text with no credentials",
		},
		{
			name:  "JWT prefix without payload is unchanged",
			input: "eyJ" + strings.Repeat("a", 10) + ".eyJ",
			want:  "eyJ" + strings.Repeat("a", 10) + ".eyJ",
		},
		{
			name:  "multiple secrets in one string",
			input: "ghp_ABCDEFGHIJKLMNOPQRSTUV and AKIAIOSFODNN7EXAMPLE both here",
			want:  "[REDACTED] and [REDACTED] both here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, Redact(tt.input), tt.want)
		})
	}
}

func TestScrub(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		literals []string
		want     string
	}{
		{
			name:     "known literal and credential pattern",
			input:    "private-value and AKIAIOSFODNN7EXAMPLE",
			literals: []string{"private-value"},
			want:     "[REDACTED] and [REDACTED]",
		},
		{
			name:     "literal replaces the whole value before pattern redaction",
			input:    "AKIAIOSFODNN7EXAMPLE-suffix",
			literals: []string{"AKIAIOSFODNN7EXAMPLE-suffix"},
			want:     "[REDACTED]",
		},
		{
			name:     "empty literal is ignored",
			input:    "clean text",
			literals: []string{""},
			want:     "clean text",
		},
		{
			name:  "patterns are redacted without literals",
			input: "AKIAIOSFODNN7EXAMPLE",
			want:  "[REDACTED]",
		},
		{
			name:     "every occurrence is redacted",
			input:    "private-value then private-value",
			literals: []string{"private-value"},
			want:     "[REDACTED] then [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, Scrub(tt.input, tt.literals...), tt.want)
		})
	}
}

// keyLine is a synthetic base64 line shaped like a PEM key body line.
var keyLine = "MIIC" + strings.Repeat("Ab1/", 15)

func TestRedactPrivateKeyBlock(t *testing.T) {
	key := "const fakePrivateKey = `-----BEGIN RSA PRIVATE KEY-----\n" + keyLine + "\n" + keyLine + "\nQsncVExbMiPa9Oclo5qLuTosS8qwHm1MJEytp3/SkB8=\n-----END RSA PRIVATE KEY-----`\n"
	got := Redact(key)
	assert.Equal(t, got, "const fakePrivateKey = `[REDACTED PRIVATE KEY BEGIN]\n[REDACTED]\n[REDACTED]\n[REDACTED]\n[REDACTED PRIVATE KEY END]`\n")

	concatenated := "key := \"-----BEGIN PRIVATE KEY-----\\n\" +\n\t\"" + keyLine + "\\n\" +\n\t\"-----END PRIVATE KEY-----\\n\""
	assert.Equal(t, Redact(concatenated), "key := \"[REDACTED PRIVATE KEY BEGIN]\\n\" +\n\t\"[REDACTED]\\n\" +\n\t\"[REDACTED PRIVATE KEY END]\\n\"")
}

func TestRedactDiffMatchesSource(t *testing.T) {
	plus := "+" + keyLine
	source := []string{keyLine, plus, "  " + plus, "Ab1/Cd2/Ef3/Gh4I", "ConfigurationV12", "x := 1"}
	for _, line := range source {
		want := Redact(line)
		for _, prefix := range []string{"+", "-", " "} {
			assert.Equal(t, RedactDiff(prefix+line), prefix+want, "line %q prefix %q", line, prefix)
		}
	}
	assert.Equal(t, Redact(plus), "[REDACTED]")
	// A trailing "+" is base64 unless it follows a closing quote.
	assert.Equal(t, Redact(keyLine+"+"), "[REDACTED]")
	assert.Equal(t, RedactDiff(" "+keyLine+"+"), " [REDACTED]")
	assert.Equal(t, Redact(`"`+keyLine+`\n" +`), `"[REDACTED]\n" +`)
	assert.Equal(t, Redact("Ab1/Cd2="), "[REDACTED]")
	assert.Equal(t, RedactDiff("-Ab1/Cd2="), "-[REDACTED]")
	assert.Equal(t, RedactDiff("+"+plus), "+[REDACTED]")
	assert.Equal(t, RedactDiff("+++ b/key.go\n@@ -1 +1 @@\n-"+keyLine), "+++ b/key.go\n@@ -1 +1 @@\n-[REDACTED]")
}

func TestRedactLeavesOrdinaryLines(t *testing.T) {
	for _, line := range []string{
		strings.Repeat("0123456789abcdef", 4),
		"thisIsAVeryLongIdentifierWithoutAnyDigitsInItAtAll",
		"h1:" + keyLine,
		"x := \"" + keyLine + "\" + y",
		"errFoo2,",
		`"Windows7",`,
		`"ubuntu2204Latest",`,
		"ConfigurationV12",
		"}",
	} {
		assert.Equal(t, Redact(line), line)
	}
}

func TestContainsPrivateKey(t *testing.T) {
	for _, text := range []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----END RSA PRIVATE KEY-----",
		`"-----BEGIN " + "PRIVATE KEY-----"`,
		"[REDACTED PRIVATE KEY BEGIN]",
		"[REDACTED PRIVATE KEY END]",
	} {
		assert.Assert(t, ContainsPrivateKey(text))
	}
	assert.Assert(t, !ContainsPrivateKey("func main() {}"))
}
