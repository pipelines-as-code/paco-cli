package reviewbench

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/review"
)

type PR struct {
	Repo     string `json:"repo"`
	PRNumber int    `json:"pr_number"`
	Base     string `json:"base"`
	Head     string `json:"head"`
}

type Finding struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Message   string `json:"message"`
	Producer  string `json:"producer"`
}

type Output struct {
	PR       PR        `json:"pr"`
	Agent    string    `json:"agent"`
	Findings []Finding `json:"findings"`
}

// Findings converts a paco review into findings on the head revision. paco
// anchors inline comments to one added line. Verified summary findings point at
// deleted lines, so they move to the nearest head line in the same hunk, and
// are dropped when the file no longer exists.
func Findings(r review.Review, parsed *diff.ParsedDiff, agent string) []Finding {
	findings := []Finding{}
	add := func(path string, line int, body string) {
		findings = append(findings, Finding{File: path, StartLine: line, EndLine: line, Message: body, Producer: agent})
	}
	for _, c := range r.Comments {
		add(c.Path, c.Line, c.Body)
	}
	for _, c := range r.SummaryFindings {
		if path, line, ok := headAnchor(parsed, c.Path, c.Line); ok {
			add(path, line, c.Body)
		}
	}
	return findings
}

func headAnchor(parsed *diff.ParsedDiff, oldPath string, oldLine int) (string, int, bool) {
	if parsed == nil {
		return "", 0, false
	}
	for _, file := range parsed.Files {
		if file.OldPath != oldPath || file.NewPath == "" {
			continue
		}
		for _, hunk := range file.Hunks {
			for i, line := range hunk.Lines {
				if line.Kind != "delete" || line.OldLine != oldLine {
					continue
				}
				for _, next := range hunk.Lines[i+1:] {
					if next.NewLine > 0 {
						return file.NewPath, next.NewLine, true
					}
				}
				for j := i - 1; j >= 0; j-- {
					if hunk.Lines[j].NewLine > 0 {
						return file.NewPath, hunk.Lines[j].NewLine, true
					}
				}
				return file.NewPath, max(hunk.NewStart, 1), true
			}
		}
	}
	return "", 0, false
}

// writeJSON replaces path atomically so a crash never leaves a partial file.
func writeJSON(path string, out any) error {
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".findings-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The harness reads the file as a different user than the container's root.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
