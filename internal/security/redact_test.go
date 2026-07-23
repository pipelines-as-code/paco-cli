package security

import (
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
