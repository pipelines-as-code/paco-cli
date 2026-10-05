package review

import (
	"encoding/json"
	"strings"
)

type Review struct {
	Summary           string      `json:"summary"`
	ReviewScore       ReviewScore `json:"review_score"`
	SecuritySensitive bool        `json:"security_sensitive"`
	Comments          []Comment   `json:"comments"`
	Verified          bool        `json:"verified,omitempty"`
	SummaryFindings   []Comment   `json:"summary_findings,omitempty"`
}

type ReviewScore struct {
	Rating int    `json:"rating"`
	Reason string `json:"reason"`
}

type Comment struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
}

// ExtractReview returns the last JSON object in text that decodes as a Review
// with a comments array, or nil.
func ExtractReview(text string) *Review {
	var lastReview *Review

	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}

		depth := 0
		inString := false
		escaped := false

		for end := start; end < len(text); end++ {
			ch := text[end]
			if inString {
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == '"' {
					inString = false
				}
				continue
			}

			if ch == '"' {
				inString = true
			} else if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					var r Review
					if json.Unmarshal([]byte(text[start:end+1]), &r) == nil && r.Comments != nil {
						lastReview = &r
						start = end
					}
					break
				}
			}
		}
	}

	return lastReview
}

const maxComments = 30

var validSeverities = map[string]bool{
	"critical": true,
	"high":     true,
	"medium":   true,
	"low":      true,
}

func Normalize(r *Review) *Review {
	var comments []Comment
	for _, c := range r.Comments {
		if c.Path == "" || c.Line == 0 || c.Body == "" {
			continue
		}
		sev := strings.ToLower(c.Severity)
		if !validSeverities[sev] {
			sev = "medium"
		}
		c.Severity = sev
		comments = append(comments, c)
	}
	if len(comments) > maxComments {
		comments = comments[:maxComments]
	}

	return &Review{
		Summary: r.Summary,
		ReviewScore: ReviewScore{
			Rating: min(max(r.ReviewScore.Rating, 1), 5),
			Reason: r.ReviewScore.Reason,
		},
		SecuritySensitive: r.SecuritySensitive,
		Comments:          comments,
		Verified:          r.Verified,
		SummaryFindings:   r.SummaryFindings,
	}
}
