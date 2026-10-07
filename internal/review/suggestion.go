package review

import (
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
)

const suggestionFence = "```suggestion"

// guardSuggestions removes suggestion blocks that GitHub would apply badly:
// malformed fences, no-op replacements, and multi-line replacements that
// repeat the lines next to the anchor. The explanation is kept; a comment
// with nothing left but the suggestion is dropped.
func guardSuggestions(comments []Comment, parsed *diff.ParsedDiff) []Comment {
	newLines := map[string]map[int]string{}
	if parsed != nil {
		for _, file := range parsed.Files {
			lines := map[int]string{}
			for _, hunk := range file.Hunks {
				for _, line := range hunk.Lines {
					if line.Kind == "add" || line.Kind == "context" {
						lines[line.NewLine] = line.Content
					}
				}
			}
			newLines[file.NewPath] = lines
		}
	}
	kept := comments[:0]
	for _, c := range comments {
		if strings.Contains(c.Body, suggestionFence) && !suggestionApplies(c, newLines[c.Path]) {
			c.Body = stripSuggestions(c.Body)
			if c.Body == "" {
				continue
			}
		}
		kept = append(kept, c)
	}
	return kept
}

func suggestionApplies(c Comment, lines map[int]string) bool {
	if strings.Count(c.Body, suggestionFence) != 1 {
		return false
	}
	_, _, replacement, ok := splitSuggestion(c.Body)
	if !ok {
		return false
	}
	current, known := lines[c.Line]
	switch {
	case !known || len(replacement) == 0:
		return true
	case len(replacement) == 1:
		return changesLine(replacement[0], current)
	}
	extra := len(replacement) - 1
	return !repeatsLines(replacement[1:], lines, c.Line+1) && !repeatsLines(replacement[:extra], lines, c.Line-extra)
}

// splitSuggestion splits body around its first suggestion fence. ok reports
// a closed fence with nothing after the opening marker; an unclosed fence
// runs to the end of body.
func splitSuggestion(body string) (before, after string, replacement []string, ok bool) {
	start := strings.Index(body, suggestionFence)
	lines := strings.SplitAfter(body[start:], "\n")
	end := start
	for i, line := range lines {
		end += len(line)
		if i > 0 && strings.TrimSpace(line) == "```" {
			for _, l := range lines[1:i] {
				replacement = append(replacement, strings.TrimSuffix(l, "\n"))
			}
			return body[:start], body[end:], replacement, strings.TrimSpace(lines[0]) == suggestionFence
		}
	}
	return body[:start], "", nil, false
}

func changesLine(replacement, current string) bool {
	return strings.TrimRight(replacement, " \t\r") != strings.TrimRight(current, " \t\r")
}

// repeatsLines reports whether want matches the known new-side lines starting
// at first, ignoring blank-only matches that carry no code.
func repeatsLines(want []string, lines map[int]string, first int) bool {
	code := false
	for i, line := range want {
		got, ok := lines[first+i]
		if !ok || strings.TrimSpace(got) != strings.TrimSpace(line) {
			return false
		}
		code = code || strings.TrimSpace(line) != ""
	}
	return code
}

func stripSuggestions(body string) string {
	for strings.Contains(body, suggestionFence) {
		before, after, _, _ := splitSuggestion(body)
		body = strings.TrimRight(before, " \t\n") + "\n\n" + strings.TrimLeft(after, " \t\n")
	}
	return strings.TrimSpace(body)
}
