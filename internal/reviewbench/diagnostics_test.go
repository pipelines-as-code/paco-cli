package reviewbench

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestScrubDiagnostic(t *testing.T) {
	value := map[string]any{
		"summary": "literal with \"quotes\"\nand newline",
		"nested":  []any{"ghp_abcdefghijklmnopqrstuvwxyz", "safe"},
	}
	scrubDiagnostic(value, []string{"literal with \"quotes\"\nand newline"})
	assert.DeepEqual(t, value, map[string]any{
		"summary": "[REDACTED]", "nested": []any{"[REDACTED]", "safe"},
	})
}
