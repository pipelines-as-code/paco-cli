package post

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"gotest.tools/v3/assert"
)

func TestBuildInlineComments(t *testing.T) {
	validLines := map[string]map[string]bool{
		"a.go": {"10": true, "20": true},
		"b.go": {"5": true},
	}
	existingInline := map[string]map[string]bool{
		"a.go": {"20": true},
	}

	tests := []struct {
		name     string
		comments []review.Comment
		want     int
	}{
		{
			name: "valid comment on added line",
			comments: []review.Comment{
				{Path: "a.go", Line: 10, Severity: "high", Body: "bug here"},
			},
			want: 1,
		},
		{
			name: "comment on non-added line dropped",
			comments: []review.Comment{
				{Path: "a.go", Line: 99, Severity: "high", Body: "not on added line"},
			},
			want: 0,
		},
		{
			name: "comment on existing inline deduplicated",
			comments: []review.Comment{
				{Path: "a.go", Line: 20, Severity: "high", Body: "already commented"},
			},
			want: 0,
		},
		{
			name: "comment on unknown file dropped",
			comments: []review.Comment{
				{Path: "unknown.go", Line: 1, Severity: "low", Body: "no such file in diff"},
			},
			want: 0,
		},
		{
			name: "malformed comment missing path dropped",
			comments: []review.Comment{
				{Path: "", Line: 10, Body: "no path"},
			},
			want: 0,
		},
		{
			name: "malformed comment missing line dropped",
			comments: []review.Comment{
				{Path: "a.go", Line: 0, Body: "no line"},
			},
			want: 0,
		},
		{
			name: "malformed comment missing body dropped",
			comments: []review.Comment{
				{Path: "a.go", Line: 10, Body: ""},
			},
			want: 0,
		},
		{
			name: "multiple comments mixed valid and invalid",
			comments: []review.Comment{
				{Path: "a.go", Line: 10, Severity: "high", Body: "valid"},
				{Path: "a.go", Line: 20, Severity: "low", Body: "deduped"},
				{Path: "b.go", Line: 5, Severity: "medium", Body: "also valid"},
				{Path: "c.go", Line: 1, Severity: "low", Body: "no such file"},
			},
			want: 2,
		},
		{
			name:     "empty comments",
			comments: []review.Comment{},
			want:     0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildInlineComments(tt.comments, validLines, existingInline)
			assert.Equal(t, len(result), tt.want)

			for _, c := range result {
				body, ok := c["body"].(string)
				assert.Assert(t, ok, "body should be a string")
				assert.Assert(t, len(body) > 0, "body should not be empty")
				assert.Equal(t, c["side"], "RIGHT")
			}
		})
	}
}

func TestBuildInlineCommentsSeverityFormatting(t *testing.T) {
	validLines := map[string]map[string]bool{"a.go": {"1": true}}
	existingInline := map[string]map[string]bool{}

	tests := []struct {
		name     string
		severity string
		wantTag  string
	}{
		{name: "high severity", severity: "high", wantTag: "**[HIGH]**"},
		{name: "low severity", severity: "low", wantTag: "**[LOW]**"},
		{name: "empty severity defaults to medium", severity: "", wantTag: "**[MEDIUM]**"},
		{name: "critical severity", severity: "critical", wantTag: "**[CRITICAL]**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comments := []review.Comment{{Path: "a.go", Line: 1, Severity: tt.severity, Body: "issue"}}
			result := buildInlineComments(comments, validLines, existingInline)
			assert.Equal(t, len(result), 1)
			body := result[0]["body"].(string)
			assert.Assert(t, len(body) > 0 && body[:len(tt.wantTag)] == tt.wantTag, "expected %s prefix, got %s", tt.wantTag, body)
		})
	}
}

func TestBuildStatusLines(t *testing.T) {
	tests := []struct {
		name             string
		failed           bool
		mode             string
		commentCount     int
		rating           int
		reason           string
		wantEmoji        string
		wantHasFindings  bool
		wantHasScore     bool
	}{
		{
			name:      "failed path",
			failed:    true,
			mode:      "review",
			wantEmoji: "⚠️",
		},
		{
			name:      "summary mode",
			failed:    false,
			mode:      "summary",
			wantEmoji: "\U0001F4DD",
		},
		{
			name:            "review with findings",
			failed:          false,
			mode:            "review",
			commentCount:    3,
			rating:          4,
			wantEmoji:       "\U0001F50D",
			wantHasFindings: true,
			wantHasScore:    true,
		},
		{
			name:            "review no findings",
			failed:          false,
			mode:            "review",
			commentCount:    0,
			rating:          1,
			wantEmoji:       "✅",
			wantHasFindings: true,
			wantHasScore:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev := &review.Review{
				ReviewScore: review.ReviewScore{Rating: tt.rating, Reason: tt.reason},
			}
			emoji, findings, score := buildStatusLines(tt.failed, tt.mode, tt.commentCount, rev, nil)
			assert.Equal(t, emoji, tt.wantEmoji)
			if tt.wantHasFindings {
				assert.Assert(t, len(findings) > 0, "expected findings line")
			}
			if tt.wantHasScore {
				assert.Assert(t, len(score) > 0, "expected score line")
			}
		})
	}
}

func TestBuildStatusLinesRatingClamping(t *testing.T) {
	tests := []struct {
		name       string
		rating     int
		wantWord   string
	}{
		{name: "rating 0 clamped to 3", rating: 0, wantWord: "Moderate"},
		{name: "rating 6 clamped to 3", rating: 6, wantWord: "Moderate"},
		{name: "rating 1", rating: 1, wantWord: "Trivial"},
		{name: "rating 5", rating: 5, wantWord: "Very Hard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev := &review.Review{ReviewScore: review.ReviewScore{Rating: tt.rating}}
			_, _, score := buildStatusLines(false, "review", 0, rev, nil)
			assert.Assert(t, len(score) > 0)
			assert.Assert(t, containsString(score, tt.wantWord), "expected %q in score line %q", tt.wantWord, score)
		})
	}
}

func TestLoadArtifactsValidation(t *testing.T) {
	tests := []struct {
		name         string
		reviewJSON   string
		validJSON    string
		existingJSON string
		wantSummary  string
		wantComments int
	}{
		{
			name:         "valid artifacts",
			reviewJSON:   `{"summary":"ok","comments":[{"path":"a.go","line":1,"severity":"low","body":"nit"}]}`,
			validJSON:    `{"a.go":{"1":true}}`,
			existingJSON: `{}`,
			wantSummary:  "ok",
			wantComments: 1,
		},
		{
			name:         "invalid review JSON uses fallback",
			reviewJSON:   `not json`,
			validJSON:    `{}`,
			existingJSON: `{}`,
			wantSummary:  "Could not parse Paco model output.",
			wantComments: 0,
		},
		{
			name:         "invalid valid lines defaults to empty",
			reviewJSON:   `{"summary":"ok","comments":[]}`,
			validJSON:    `not json`,
			existingJSON: `{}`,
			wantSummary:  "ok",
			wantComments: 0,
		},
		{
			name:         "invalid existing inline defaults to empty",
			reviewJSON:   `{"summary":"ok","comments":[]}`,
			validJSON:    `{}`,
			existingJSON: `not json`,
			wantSummary:  "ok",
			wantComments: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			writeFile(t, ws, ".paco-review.json", tt.reviewJSON)
			writeFile(t, ws, ".valid-lines.json", tt.validJSON)
			writeFile(t, ws, ".existing-inline.json", tt.existingJSON)

			a := &artifact.Workspace{Dir: ws}
			rev, validLines, existingInline, err := loadArtifacts(a)
			assert.NilError(t, err)
			assert.Equal(t, rev.Summary, tt.wantSummary)
			assert.Equal(t, len(rev.Comments), tt.wantComments)
			assert.Assert(t, validLines != nil)
			assert.Assert(t, existingInline != nil)
		})
	}
}

func TestApplyLabelsTargetSelection(t *testing.T) {
	tests := []struct {
		rating int
		want   string
	}{
		{1, "paco/review-trivial"},
		{2, "paco/review-easy"},
		{3, "paco/review-moderate"},
		{4, "paco/review-hard"},
		{5, "paco/review-very-hard"},
	}
	for _, tt := range tests {
		t.Run("rating "+strconv.Itoa(tt.rating), func(t *testing.T) {
			var targetLabel string
			for _, sl := range scoreLabels {
				if sl.Rating == tt.rating {
					targetLabel = sl.Name
					break
				}
			}
			assert.Equal(t, targetLabel, tt.want)
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := dir + "/" + name
	if err := json.Unmarshal([]byte(content), &json.RawMessage{}); err != nil {
		// not JSON, write raw
	}
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o600))
}
