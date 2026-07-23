package diff

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestBuildExistingInlineMap(t *testing.T) {
	permMap := map[string]string{
		"trusted":    "write",
		"admin":      "admin",
		"outsider":   "read",
		"maintainer": "maintain",
	}

	comments := []inlineComment{
		{Login: "trusted", Path: "a.go", Line: 10, Resolved: false, ReviewState: "COMMENTED"},
		{Login: "outsider", Path: "b.go", Line: 20, Resolved: false, ReviewState: "COMMENTED"},
		{Login: "trusted", Path: "c.go", Line: 30, Resolved: true, ReviewState: "COMMENTED"},
		{Login: "trusted", Path: "d.go", Line: 40, Resolved: false, ReviewState: "DISMISSED"},
		{Login: "admin", Path: "e.go", Line: 50, Resolved: false, ReviewState: "APPROVED"},
		{Login: "maintainer", Path: "f.go", Line: 60, Resolved: false, ReviewState: "COMMENTED"},
	}

	result := buildExistingInlineMap(comments, permMap)

	assert.Assert(t, result["a.go"]["10"], "trusted write comment should be included")
	assert.Assert(t, result["b.go"] == nil, "read-only user comment should be excluded")
	assert.Assert(t, result["c.go"] == nil, "resolved thread should be excluded")
	assert.Assert(t, result["d.go"] == nil, "dismissed review should be excluded")
	assert.Assert(t, result["e.go"]["50"], "admin comment should be included")
	assert.Assert(t, result["f.go"]["60"], "maintainer comment should be included")
}

func TestBuildFeedbackDigest(t *testing.T) {
	permMap := map[string]string{
		"trusted":  "write",
		"outsider": "read",
	}

	comments := []inlineComment{
		{Login: "trusted", Path: "a.go", Line: 10, Body: "good point", Resolved: false, ReviewState: "COMMENTED"},
		{Login: "outsider", Path: "b.go", Line: 20, Body: "my opinion", Resolved: false, ReviewState: "COMMENTED"},
		{Login: "trusted", Path: "c.go", Line: 30, Body: "<!-- paco-review -->summary", Resolved: false, ReviewState: "COMMENTED"},
	}

	digest := buildFeedbackDigest(comments, nil, nil, permMap)

	assert.Assert(t, strings.Contains(digest, "trusted on a.go:10"), "trusted comment should be in digest")
	assert.Assert(t, !strings.Contains(digest, "outsider"), "untrusted comment should be excluded")
	assert.Assert(t, !strings.Contains(digest, "paco-review"), "paco marker comment should be excluded")
}

func TestIsTrusted(t *testing.T) {
	tests := []struct {
		perm string
		want bool
	}{
		{"write", true},
		{"admin", true},
		{"maintain", true},
		{"read", false},
		{"none", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.perm, func(t *testing.T) {
			assert.Equal(t, isTrusted(tt.perm), tt.want)
		})
	}
}

func TestCollectLogins(t *testing.T) {
	comments := []inlineComment{
		{Login: "alice"},
		{Login: "bob"},
		{Login: "alice"},
	}

	reviews := []map[string]interface{}{
		{"user": map[string]interface{}{"login": "charlie"}},
	}

	issueComments := []map[string]interface{}{
		{"user": map[string]interface{}{"login": "bob"}},
	}

	logins := collectLogins(comments, reviews, issueComments)
	loginMap := map[string]bool{}
	for _, l := range logins {
		loginMap[l] = true
	}

	assert.Assert(t, loginMap["alice"])
	assert.Assert(t, loginMap["bob"])
	assert.Assert(t, loginMap["charlie"])
	assert.Equal(t, len(logins), 3)
}
