package security

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestScanSecrets(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		extraLiterals []string
		want          string
	}{
		{
			name: "clean text",
			text: "this is perfectly clean output with no secrets",
			want: "",
		},
		{
			name: "empty text",
			text: "",
			want: "",
		},
		{
			name: "github token ghp",
			text: "found ghp_ABCDEFghijklmnopqrstuvwx in output",
			want: "github-token-pattern",
		},
		{
			name: "github token gho",
			text: "found gho_ABCDEFghijklmnopqrstuvwx in output",
			want: "github-token-pattern",
		},
		{
			name: "github token ghs",
			text: "found ghs_ABCDEFghijklmnopqrstuvwx in output",
			want: "github-token-pattern",
		},
		{
			name: "github token ghu",
			text: "found ghu_ABCDEFghijklmnopqrstuvwx in output",
			want: "github-token-pattern",
		},
		{
			name: "github token ghr",
			text: "found ghr_ABCDEFghijklmnopqrstuvwx in output",
			want: "github-token-pattern",
		},
		{
			name: "github token too short does not match",
			text: "ghp_shorttoken123",
			want: "",
		},
		{
			name: "github PAT",
			text: "github_pat_ABCDEFGHIJ1234567890_morestuff",
			want: "github-pat-pattern",
		},
		{
			name: "github PAT too short does not match",
			text: "github_pat_short",
			want: "",
		},
		{
			name: "JWT",
			text: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0",
			want: "jwt-pattern",
		},
		{
			name: "JWT-like but not valid prefix does not match",
			text: "eyXnotajwt.eyXnotajwt",
			want: "",
		},
		{
			name: "AWS access key",
			text: "key is AKIAIOSFODNN7EXAMPLE",
			want: "aws-key-pattern",
		},
		{
			name: "AWS key wrong prefix does not match",
			text: "BKIAIOSFODNN7EXAMPLE",
			want: "",
		},
		{
			name: "GCP service account email",
			text: "my-sa@my-project.iam.gserviceaccount.com",
			want: "google-service-account-pattern",
		},
		{
			name: "regular email does not match",
			text: "user@example.com",
			want: "",
		},
		{
			name: "PEM RSA private key header",
			text: "-----BEGIN RSA PRIVATE KEY-----",
			want: "private-key-pattern",
		},
		{
			name: "PEM EC private key header",
			text: "-----BEGIN EC PRIVATE KEY-----",
			want: "private-key-pattern",
		},
		{
			name: "PEM generic private key header",
			text: "-----BEGIN PRIVATE KEY-----",
			want: "private-key-pattern",
		},
		{
			name: "PEM public key does not match",
			text: "-----BEGIN PUBLIC KEY-----",
			want: "",
		},
		{
			name: "secret embedded in JSON",
			text: `{"token": "ghp_ABCDEFghijklmnopqrstuvwx", "ok": true}`,
			want: "github-token-pattern",
		},
		{
			name: "secret embedded in multiline output",
			text: "line 1\nline 2\nghp_ABCDEFghijklmnopqrstuvwx\nline 4",
			want: "github-token-pattern",
		},
		{
			name: "multiple secrets returns first matching rule",
			text: "ghp_ABCDEFghijklmnopqrstuvwx and AKIAIOSFODNN7EXAMPLE",
			want: "github-token-pattern",
		},
		{
			name:          "extra literal match takes priority",
			text:          "ghp_ABCDEFghijklmnopqrstuvwx and the-secret-literal",
			extraLiterals: []string{"the-secret-literal"},
			want:          "known-literal-match",
		},
		{
			name:          "extra literal exact match",
			text:          "output contains vertex-sa@proj.iam.gserviceaccount.com literally",
			extraLiterals: []string{"vertex-sa@proj.iam.gserviceaccount.com"},
			want:          "known-literal-match",
		},
		{
			name:          "extra literal no match falls through to patterns",
			text:          "ghp_ABCDEFghijklmnopqrstuvwx but not the literal",
			extraLiterals: []string{"nonexistent-literal"},
			want:          "github-token-pattern",
		},
		{
			name:          "empty extra literal is ignored",
			text:          "clean text",
			extraLiterals: []string{""},
			want:          "",
		},
		{
			name:          "multiple extra literals first match wins",
			text:          "contains second-secret in text",
			extraLiterals: []string{"first-secret", "second-secret"},
			want:          "known-literal-match",
		},
		{
			name:          "no extra literals passed",
			text:          "clean text",
			extraLiterals: nil,
			want:          "",
		},
		{
			name: "partial pattern at boundary does not match",
			text: "ghp_tooshort",
			want: "",
		},
		{
			name: "secret surrounded by special characters",
			text: "[ghp_ABCDEFghijklmnopqrstuvwx]",
			want: "github-token-pattern",
		},
		{
			name: "very large input with secret at end",
			text: strings.Repeat("a", 100000) + "ghp_ABCDEFghijklmnopqrstuvwx",
			want: "github-token-pattern",
		},
		{
			name: "very large clean input",
			text: strings.Repeat("no secrets here ", 10000),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ScanSecrets(tt.text, tt.extraLiterals...)
			assert.Equal(t, result, tt.want)
		})
	}
}
