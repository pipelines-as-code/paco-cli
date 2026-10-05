package review

// reviewSchema constrains the model output to the Review shape through
// structured outputs. It only uses JSON Schema features the API accepts, so
// numeric ranges and the comment cap are still enforced by Normalize.
var reviewSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"summary", "review_score", "security_sensitive", "comments"},
	"properties": map[string]any{
		"summary": map[string]any{"type": "string"},
		"review_score": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"rating", "reason"},
			"properties": map[string]any{
				"rating": map[string]any{"type": "integer"},
				"reason": map[string]any{"type": "string"},
			},
		},
		"security_sensitive": map[string]any{"type": "boolean"},
		"comments": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path", "line", "severity", "body"},
				"properties": map[string]any{
					"path":     map[string]any{"type": "string"},
					"line":     map[string]any{"type": "integer"},
					"severity": map[string]any{"type": "string", "enum": []any{"critical", "high", "medium", "low"}},
					"body":     map[string]any{"type": "string"},
				},
			},
		},
	},
}
