package review

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/skill"
)

// parseTrigger reads the mode and requested skill names from the trigger
// comment. Pipelines-as-Code may pass line breaks as literal \n.
func parseTrigger(comment string) (mode string, requested []string, err error) {
	mode = "review"
	line := strings.ReplaceAll(comment, `\n`, "\n")
	if first, _, ok := strings.Cut(line, "\n"); ok {
		line = first
	}
	fields := strings.Fields(line)
	start := min(1, len(fields))
	if len(fields) > 1 && (strings.EqualFold(fields[1], "review") || strings.EqualFold(fields[1], "summary")) {
		if strings.EqualFold(fields[1], "summary") {
			mode = "summary"
		}
		start = 2
	}
	for _, field := range fields[start:] {
		name, ok := strings.CutPrefix(field, "skill:")
		if !ok {
			continue
		}
		if err := skill.ValidName(name); err != nil {
			return "", nil, fmt.Errorf("invalid skill name %q: %w", name, err)
		}
		if slices.Contains(requested, name) {
			continue
		}
		if len(requested) >= skill.MaxRequested {
			return "", nil, fmt.Errorf("at most %d skills can be requested", skill.MaxRequested)
		}
		requested = append(requested, name)
	}
	return mode, requested, nil
}
