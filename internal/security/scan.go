package security

import (
	"regexp"
	"strings"
)

type ScanRule struct {
	Name    string
	Pattern *regexp.Regexp
}

var scanRules = []ScanRule{
	{Name: "github-token-pattern", Pattern: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{Name: "github-pat-pattern", Pattern: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{Name: "jwt-pattern", Pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ`)},
	{Name: "aws-key-pattern", Pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{Name: "google-service-account-pattern", Pattern: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.iam\.gserviceaccount\.com`)},
	{Name: "private-key-pattern", Pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

func ScanSecrets(text string, extraLiterals ...string) string {
	for _, literal := range extraLiterals {
		if literal != "" && strings.Contains(text, literal) {
			return "known-literal-match"
		}
	}
	for _, rule := range scanRules {
		if rule.Pattern.MatchString(text) {
			return rule.Name
		}
	}
	return ""
}
