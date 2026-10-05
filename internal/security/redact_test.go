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
			want:  "[REDACTED]",
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
