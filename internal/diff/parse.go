package diff

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/source"
)

type (
	ParsedDiff = source.Diff
	FileDiff   = source.FileDiff
	Hunk       = source.Hunk
	DiffLine   = source.DiffLine
)

var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?:.*)$`)

// Parse preserves line content verbatim. Incomplete hunks remain usable for
// legacy diffs, but are explicitly marked rather than claiming full coverage.
func Parse(text string) (*ParsedDiff, error) {
	if len(text) > maxDiffBytes {
		return nil, errors.New("diff exceeds 200000 bytes")
	}
	result := &ParsedDiff{Files: []FileDiff{}}
	var file *FileDiff
	var hunk *Hunk
	var oldLine, newLine, oldSeen, newSeen int
	finish := func() {
		if hunk != nil {
			hunk.Complete = oldSeen == hunk.OldCount && newSeen == hunk.NewCount
			hunk = nil
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			finish()
			oldPath, newPath, err := diffPaths(strings.TrimPrefix(line, "diff --git "))
			if err != nil {
				return nil, err
			}
			result.Files = append(result.Files, FileDiff{OldPath: oldPath, NewPath: newPath, Status: "modified", Hunks: []Hunk{}})
			file = &result.Files[len(result.Files)-1]
			continue
		}
		if strings.HasPrefix(line, "@@") {
			finish()
			if file == nil {
				return nil, errors.New("diff hunk has no file")
			}
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, errors.New("invalid diff hunk header")
			}
			var nums [4]int
			for i, value := range m[1:5] {
				if value == "" {
					nums[i] = 1
					continue
				}
				n, err := strconv.Atoi(value)
				if err != nil || n > int(^uint(0)>>1)-maxDiffBytes {
					return nil, errors.New("diff line coordinate overflows")
				}
				nums[i] = n
			}
			if (nums[0] == 0 && nums[1] != 0) || (nums[2] == 0 && nums[3] != 0) {
				return nil, errors.New("invalid zero diff line coordinate")
			}
			file.Hunks = append(file.Hunks, Hunk{OldStart: nums[0], OldCount: nums[1], NewStart: nums[2], NewCount: nums[3], Lines: []DiffLine{}})
			hunk = &file.Hunks[len(file.Hunks)-1]
			oldLine, newLine, oldSeen, newSeen = nums[0], nums[2], 0, 0
			continue
		}
		if hunk != nil {
			if line == `\ No newline at end of file` {
				if len(hunk.Lines) == 0 {
					return nil, errors.New("newline marker has no preceding diff line")
				}
				hunk.Lines[len(hunk.Lines)-1].NoNewline = true
				continue
			}
			if line != "" {
				entry := DiffLine{Content: line[1:]}
				switch line[0] {
				case ' ':
					entry.Kind, entry.OldLine, entry.NewLine = "context", oldLine, newLine
					oldLine++
					newLine++
					oldSeen++
					newSeen++
				case '-':
					entry.Kind, entry.OldLine = "delete", oldLine
					oldLine++
					oldSeen++
				case '+':
					entry.Kind, entry.NewLine = "add", newLine
					newLine++
					newSeen++
				default:
					return nil, errors.New("invalid diff hunk line")
				}
				if oldSeen > hunk.OldCount || newSeen > hunk.NewCount {
					return nil, errors.New("diff hunk exceeds declared line counts")
				}
				hunk.Lines = append(hunk.Lines, entry)
				continue
			}
			finish()
		}
		if file == nil {
			if line != "" {
				return nil, errors.New("diff content has no file header")
			}
			continue
		}
		var err error
		switch {
		case strings.HasPrefix(line, "--- "):
			file.OldPath, err = headerPath(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			file.NewPath, err = headerPath(line[4:], "b/")
		case strings.HasPrefix(line, "rename from "):
			file.OldPath, err = decodePath(strings.TrimPrefix(line, "rename from "))
			file.Status = "renamed"
		case strings.HasPrefix(line, "rename to "):
			file.NewPath, err = decodePath(strings.TrimPrefix(line, "rename to "))
			file.Status = "renamed"
		case strings.HasPrefix(line, "copy from "):
			file.OldPath, err = decodePath(strings.TrimPrefix(line, "copy from "))
			file.Status = "copied"
		case strings.HasPrefix(line, "copy to "):
			file.NewPath, err = decodePath(strings.TrimPrefix(line, "copy to "))
			file.Status = "copied"
		case strings.HasPrefix(line, "new file mode "):
			file.OldPath, file.Status = "", "added"
			file.NewMode = strings.TrimPrefix(line, "new file mode ")
		case strings.HasPrefix(line, "deleted file mode "):
			file.NewPath, file.Status = "", "deleted"
			file.OldMode = strings.TrimPrefix(line, "deleted file mode ")
		case strings.HasPrefix(line, "old mode "):
			file.OldMode = strings.TrimPrefix(line, "old mode ")
		case strings.HasPrefix(line, "new mode "):
			file.NewMode = strings.TrimPrefix(line, "new mode ")
		case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
			file.Binary = true
		}
		if err != nil {
			return nil, err
		}
		if file.OldPath == "" {
			file.Status = "added"
		} else if file.NewPath == "" {
			file.Status = "deleted"
		}
	}
	finish()
	return result, nil
}

// Limitations lists coverage gaps in parsed that reviewers should report:
// one note for binary changes and one per file with an incomplete hunk.
func Limitations(parsed *ParsedDiff) []string {
	var notes []string
	for _, file := range parsed.Files {
		if file.Binary {
			notes = append(notes, "Binary file changes have no text hunk context.")
			break
		}
	}
	for _, file := range parsed.Files {
		for _, hunk := range file.Hunks {
			if !hunk.Complete {
				notes = append(notes, "A diff hunk is incomplete.")
				break
			}
		}
	}
	return notes
}

func ParseValidLines(r io.Reader) (map[string]map[string]bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDiffBytes+1))
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(string(data))
	if err != nil {
		return nil, err
	}
	return ValidLines(parsed), nil
}

func ValidLines(parsed *ParsedDiff) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for _, file := range parsed.Files {
		if file.NewPath == "" {
			continue
		}
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				if line.Kind == "add" {
					if result[file.NewPath] == nil {
						result[file.NewPath] = map[string]bool{}
					}
					result[file.NewPath][strconv.Itoa(line.NewLine)] = true
				}
			}
		}
	}
	return result
}

func decodePath(text string) (string, error) {
	if strings.HasPrefix(text, `"`) {
		value, err := strconv.Unquote(text)
		if err != nil {
			return "", errors.New("invalid quoted diff path")
		}
		return value, nil
	}
	if text == "" {
		return "", errors.New("empty diff path")
	}
	return text, nil
}

func headerPath(text, prefix string) (string, error) {
	// Git terminates unquoted space-containing ---/+++ paths with a tab.
	name, err := decodePath(strings.TrimSuffix(text, "\t"))
	if err != nil {
		return "", err
	}
	if name == "/dev/null" {
		return "", nil
	}
	if !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("diff path is missing %s prefix", prefix)
	}
	return strings.TrimPrefix(name, prefix), nil
}

func diffPaths(text string) (string, string, error) {
	var left, right string
	if strings.HasPrefix(text, `"`) {
		end := 1
		for ; end < len(text); end++ {
			if text[end] == '\\' {
				end++
			} else if text[end] == '"' {
				break
			}
		}
		if end >= len(text) || end+1 >= len(text) || text[end+1] != ' ' {
			return "", "", errors.New("invalid quoted diff file header")
		}
		left, right = text[:end+1], text[end+2:]
	} else {
		// Git leaves spaces unquoted. The b/ delimiter, not whitespace,
		// separates the two paths in that representation.
		i := strings.Index(text, " b/")
		middle := len(text) / 2
		if strings.HasPrefix(text, "a/") && strings.HasPrefix(text[middle:], " b/") &&
			text[2:middle] == text[middle+3:] {
			i = middle
		}
		if i < 0 {
			i = strings.Index(text, ` "b/`)
		}
		if i < 0 {
			return "", "", errors.New("invalid diff file header")
		}
		left, right = text[:i], text[i+1:]
	}
	oldPath, err := headerPath(left, "a/")
	if err != nil {
		return "", "", err
	}
	newPath, err := headerPath(right, "b/")
	return oldPath, newPath, err
}
