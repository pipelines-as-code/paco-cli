package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties, "required": required,
	}
}

func evidenceSchema() map[string]any {
	return map[string]any{"type": "array", "items": objectSchema(map[string]any{
		"revision": map[string]any{"type": "string", "enum": []string{"head", "before"}},
		"path":     map[string]any{"type": "string"},
		"start":    map[string]any{"type": "integer"},
		"end":      map[string]any{"type": "integer"},
		"quote":    map[string]any{"type": "string"},
	}, "revision", "path", "start", "end", "quote")}
}

func discoverySchema() map[string]any {
	candidate := objectSchema(map[string]any{
		"id":       map[string]any{"type": "string"},
		"path":     map[string]any{"type": "string"},
		"line":     map[string]any{"type": "integer"},
		"side":     map[string]any{"type": "string", "enum": []string{"head", "before"}},
		"severity": map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low"}},
		"claim":    map[string]any{"type": "string"},
		"trigger":  map[string]any{"type": "string"},
		"impact":   map[string]any{"type": "string"},
		"remedy":   map[string]any{"type": "string"},
		"evidence": evidenceSchema(),
	}, "id", "path", "line", "side", "severity", "claim", "trigger", "impact", "remedy", "evidence")
	return objectSchema(map[string]any{
		"summary": map[string]any{"type": "string"},
		"review_score": objectSchema(map[string]any{
			"rating": map[string]any{"type": "integer"},
			"reason": map[string]any{"type": "string"},
		}, "rating", "reason"),
		"security_sensitive": map[string]any{"type": "boolean"},
		"overflow":           map[string]any{"type": "boolean"},
		"candidates":         map[string]any{"type": "array", "items": candidate},
	}, "summary", "review_score", "security_sensitive", "overflow", "candidates")
}

// The API schema is optional, so the same shape must also be checked locally.
func validateShape(text string, schema map[string]any) error {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return errors.New("response is not a JSON object")
	}
	return checkShape(value, schema)
}

func checkShape(value any, schema map[string]any) error {
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("expected an object")
		}
		required, _ := schema["required"].([]string)
		for _, key := range required {
			if _, exists := object[key]; !exists {
				return fmt.Errorf("response missing required field %s", key)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for key, item := range object {
			property, ok := properties[key].(map[string]any)
			if !ok {
				return errors.New("response contains an unknown field")
			}
			if err := checkShape(item, property); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return errors.New("expected an array")
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return errors.New("invalid internal array schema")
		}
		for _, item := range array {
			if err := checkShape(item, items); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return errors.New("expected a string")
		}
		if values, ok := schema["enum"].([]string); ok {
			for _, allowed := range values {
				if text == allowed {
					return nil
				}
			}
			return errors.New("unexpected enumeration value")
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return errors.New("expected an integer")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("expected a boolean")
		}
	default:
		return errors.New("unsupported internal schema type")
	}
	return nil
}

func verdictSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary": map[string]any{"type": "string"},
		"decisions": map[string]any{"type": "array", "items": objectSchema(map[string]any{
			"id":           map[string]any{"type": "string"},
			"outcome":      map[string]any{"type": "string", "enum": []string{"accept", "reject", "insufficient_evidence"}},
			"reason":       map[string]any{"type": "string"},
			"duplicate_of": map[string]any{"type": "string"},
			"evidence":     evidenceSchema(),
		}, "id", "outcome", "reason", "duplicate_of", "evidence")},
	}, "summary", "decisions")
}
