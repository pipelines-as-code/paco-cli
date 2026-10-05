package diff

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
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

	reviews := []ghclient.Review{{Login: "charlie"}, {Login: ""}}
	issueComments := []ghclient.IssueComment{{Login: "bob"}}

	assert.DeepEqual(t, collectLogins(comments, reviews, issueComments), []string{"alice", "bob", "charlie"})
}

func TestBuildFeedbackDigestGolden(t *testing.T) {
	permMap := map[string]string{"alice": "write", "bob": "admin", "eve": "read"}
	long := strings.Repeat("x", 450)
	comments := []inlineComment{
		{Login: "alice", Path: "a.go", Line: 3, Body: "line one\r\nline two", ReviewState: "COMMENTED"},
		{Login: "alice", Path: "b.go", Line: 4, Body: "resolved", Resolved: true, ReviewState: "COMMENTED"},
		{Login: "alice", Path: "c.go", Line: 5, Body: "dismissed", ReviewState: "DISMISSED"},
		{Login: "eve", Path: "d.go", Line: 6, Body: "untrusted", ReviewState: "COMMENTED"},
		{Login: "bob", Path: "e.go", Line: 7, Body: long, ReviewState: "APPROVED"},
		{Login: "bob", Path: "", Line: 8, Body: "no path", ReviewState: "COMMENTED"},
	}
	reviews := []ghclient.Review{
		{Login: "bob", Body: "LGTM\nship it", State: "APPROVED"},
		{Login: "bob", Body: "## Paco Review\nold", State: "COMMENTED"},
		{Login: "bob", Body: "Paco inline comments for x", State: "COMMENTED"},
		{Login: "bob", Body: "<!-- paco-review --> marker", State: "COMMENTED"},
		{Login: "alice", Body: "dismissed review", State: "DISMISSED"},
		{Login: "", Body: "ghost review", State: "COMMENTED"},
		{Login: "alice", Body: "", State: "APPROVED"},
	}
	issueComments := []ghclient.IssueComment{
		{ID: 1, Login: "alice", Body: "please add tests"},
		{ID: 2, Login: "eve", Body: "drive-by"},
		{ID: 3, Login: "bob", Body: "<!-- paco-review -->\nsticky"},
	}

	want := strings.Join([]string{
		"- alice on a.go:3: line one  line two",
		"- bob on e.go:7: " + strings.Repeat("x", 400),
		"- review by bob: LGTM ship it",
		"- comment by alice: please add tests",
	}, "\n")
	assert.Equal(t, buildFeedbackDigest(comments, reviews, issueComments, permMap), want)

	inline, err := json.Marshal(buildExistingInlineMap(comments, permMap))
	assert.NilError(t, err)
	assert.Equal(t, string(inline), `{"a.go":{"3":true},"e.go":{"7":true}}`)
}
