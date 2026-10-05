package security

import (
	"regexp"
	"strings"
)

var scanRules = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"github-token-pattern", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"github-pat-pattern", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{"jwt-pattern", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ`)},
	{"aws-key-pattern", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"anthropic-key-pattern", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"google-service-account-pattern", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.iam\.gserviceaccount\.com`)},
	{"private-key-pattern", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// ScanSecrets returns the name of the first rule matching text, or "".
func ScanSecrets(text string, extraLiterals ...string) string {
	for _, literal := range extraLiterals {
		if literal != "" && strings.Contains(text, literal) {
			return "known-literal-match"
		}
	}
	for _, rule := range scanRules {
		if rule.pattern.MatchString(text) {
			return rule.name
		}
	}
	return ""
}
