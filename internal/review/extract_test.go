package review

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestExtractReview(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantNil     bool
		wantSummary string
		wantCount   int
	}{
		{
			name:        "clean JSON",
			input:       `{"summary":"looks good","comments":[]}`,
			wantSummary: "looks good",
			wantCount:   0,
		},
		{
			name:        "JSON with prose prefix",
			input:       "Here is my review:\n\n" + `{"summary":"found issues","comments":[{"path":"a.go","line":1,"severity":"high","body":"bug"}]}`,
			wantSummary: "found issues",
			wantCount:   1,
		},
		{
			name:        "multiple JSON objects keeps last valid",
			input:       `{"summary":"first","comments":[]} some text {"summary":"second","comments":[{"path":"b.go","line":2,"severity":"low","body":"nit"}]}`,
			wantSummary: "second",
			wantCount:   1,
		},
		{
			name:    "no valid JSON",
			input:   "this is not json at all",
			wantNil: true,
		},
		{
			name:    "JSON without comments array",
			input:   `{"summary":"no comments field"}`,
			wantNil: true,
		},
		{
			name:        "braces inside strings",
			input:       `{"summary":"use {braces} here","comments":[]}`,
			wantSummary: "use {braces} here",
			wantCount:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractReview(tt.input)
			assert.NilError(t, err)
			if tt.wantNil {
				assert.Assert(t, result == nil, "expected nil result")
				return
			}
			assert.Assert(t, result != nil, "expected non-nil result")
			assert.Equal(t, result.Summary, tt.wantSummary)
			assert.Equal(t, len(result.Comments), tt.wantCount)
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name       string
		input      Review
		wantRating int
		wantCount  int
	}{
		{
			name: "clamp rating below 1",
			input: Review{
				Summary:     "test",
				ReviewScore: ReviewScore{Rating: 0},
				Comments:    []Comment{},
			},
			wantRating: 1,
			wantCount:  0,
		},
		{
			name: "clamp rating above 5",
			input: Review{
				Summary:     "test",
				ReviewScore: ReviewScore{Rating: 99},
				Comments:    []Comment{},
			},
			wantRating: 5,
			wantCount:  0,
		},
		{
			name: "drop malformed comments",
			input: Review{
				Summary:     "test",
				ReviewScore: ReviewScore{Rating: 3},
				Comments: []Comment{
					{Path: "a.go", Line: 1, Body: "valid", Severity: "high"},
					{Path: "", Line: 1, Body: "no path"},
					{Path: "b.go", Line: 0, Body: "no line"},
					{Path: "c.go", Line: 2, Body: ""},
				},
			},
			wantRating: 3,
			wantCount:  1,
		},
		{
			name: "clamp unknown severity to medium",
			input: Review{
				Summary:     "test",
				ReviewScore: ReviewScore{Rating: 3},
				Comments: []Comment{
					{Path: "a.go", Line: 1, Body: "bug", Severity: "EXTREME"},
				},
			},
			wantRating: 3,
			wantCount:  1,
		},
		{
			name: "cap at 30 comments",
			input: func() Review {
				r := Review{Summary: "test", ReviewScore: ReviewScore{Rating: 3}}
				for i := 1; i <= 35; i++ {
					r.Comments = append(r.Comments, Comment{Path: "a.go", Line: i, Body: "issue", Severity: "low"})
				}
				return r
			}(),
			wantRating: 3,
			wantCount:  30,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Normalize(&tt.input)
			assert.Equal(t, result.ReviewScore.Rating, tt.wantRating)
			assert.Equal(t, len(result.Comments), tt.wantCount)
		})
	}
}
