package source

import (
	"errors"
	"strings"
)

// Reverse rebuilds the old side of d by undoing it on head, for callers that
// have a working tree but no access to the comparison revision. Files the
// diff cannot reproduce exactly (binary, incomplete hunks, missing from head,
// or mismatched content) are left out and counted as excluded.
func Reverse(head *Snapshot, d *Diff, commit string) *Snapshot {
	before := &Snapshot{Commit: commit, Files: make(map[string]string, len(head.Files)), Excluded: head.Excluded}
	for name, content := range head.Files {
		before.Files[name] = content
	}
	// Remove every new-side path first so renames that swap names still work.
	for _, file := range d.Files {
		if file.NewPath != "" && file.Status != "copied" {
			delete(before.Files, file.NewPath)
		}
	}
	for _, file := range d.Files {
		switch {
		case file.Status == "added" || file.Status == "copied":
			continue
		case file.Binary || !safePath(file.OldPath) || excludedPath(file.OldPath):
			before.Excluded++
			continue
		}
		var content string
		var err error
		if file.Status == "deleted" {
			content, err = deletedContent(file.Hunks)
		} else {
			current, ok := head.Files[file.NewPath]
			if !ok {
				before.Excluded++
				continue
			}
			content, err = unapply(current, file.Hunks)
		}
		if err != nil || len(content) > maxFileBytes {
			before.Excluded++
			continue
		}
		before.Files[file.OldPath] = content
	}
	return before
}

func deletedContent(hunks []Hunk) (string, error) {
	var b strings.Builder
	for _, hunk := range hunks {
		if !hunk.Complete {
			return "", errors.New("incomplete hunk")
		}
		for _, line := range hunk.Lines {
			if line.Kind != "delete" {
				return "", errors.New("deleted file hunk has new-side lines")
			}
			b.WriteString(line.Content)
			if !line.NoNewline {
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

// unapply returns the old file content, checking every context and added line
// against the new content so a stale or mismatched diff is never trusted.
func unapply(content string, hunks []Hunk) (string, error) {
	newTrailing := strings.HasSuffix(content, "\n")
	var lines []string
	if content != "" {
		lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	}
	oldTrailing := newTrailing
	var out []string
	pos := 0
	for _, hunk := range hunks {
		if !hunk.Complete {
			return "", errors.New("incomplete hunk")
		}
		start := hunk.NewStart - 1
		if hunk.NewCount == 0 {
			start = hunk.NewStart
		}
		if start < pos || start > len(lines) {
			return "", errors.New("hunk out of order or out of range")
		}
		out = append(out, lines[pos:start]...)
		pos = start
		oldMarker := false
		for _, line := range hunk.Lines {
			if line.Kind != "delete" {
				if pos >= len(lines) || lines[pos] != line.Content {
					return "", errors.New("diff does not match head content")
				}
				pos++
			}
			if line.Kind != "add" {
				out = append(out, line.Content)
				oldMarker = line.NoNewline
			}
		}
		if pos == len(lines) {
			oldTrailing = !oldMarker
		}
	}
	out = append(out, lines[pos:]...)
	if len(out) == 0 {
		return "", nil
	}
	result := strings.Join(out, "\n")
	if oldTrailing {
		result += "\n"
	}
	return result, nil
}
