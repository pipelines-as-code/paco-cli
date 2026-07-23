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

func ExtractReview(text string) (*Review, error) {
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
					candidate := text[start : end+1]
					var r Review
					if err := json.Unmarshal([]byte(candidate), &r); err == nil {
						if r.Comments != nil {
							lastReview = &r
							start = end
						}
					}
					break
				}
			}
		}
	}

	if lastReview == nil {
		return nil, nil
	}
	return lastReview, nil
}

const maxComments = 30

var validSeverities = map[string]bool{
	"critical": true,
	"high":     true,
	"medium":   true,
	"low":      true,
}

func Normalize(r *Review) *Review {
	rating := r.ReviewScore.Rating
	if rating < 1 {
		rating = 1
	}
	if rating > 5 {
		rating = 5
	}

	reason := r.ReviewScore.Reason

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
			Rating: rating,
			Reason: reason,
		},
		SecuritySensitive: r.SecuritySensitive,
		Comments:          comments,
	}
}
