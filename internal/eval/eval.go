// Package eval runs explicitly selected review fixtures without GitHub writes.
package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/source"
)

type Issue struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Side  string `json:"side"`
	Claim string `json:"claim"`
}

type Case struct {
	Name   string            `json:"name"`
	Split  string            `json:"split"`
	Before map[string]string `json:"before"`
	Head   map[string]string `json:"head"`
	Issues []Issue           `json:"issues"`
}

type Run struct {
	ID               string                     `json:"id"`
	Case             string                     `json:"case"`
	Split            string                     `json:"split"`
	Strategy         string                     `json:"strategy"`
	Model            string                     `json:"model"`
	Effort           string                     `json:"effort"`
	InputDigest      string                     `json:"input_digest"`
	PromptDigest     string                     `json:"prompt_digest"`
	StructuredOutput bool                       `json:"structured_output"`
	Milliseconds     int64                      `json:"milliseconds"`
	Usage            model.Usage                `json:"usage"`
	Failed           bool                       `json:"failed"`
	Error            string                     `json:"error,omitempty"`
	Review           review.Review              `json:"review"`
	Status           *review.VerificationStatus `json:"status,omitempty"`
	Expected         []Issue                    `json:"expected"`
}

type Report struct {
	Version int    `json:"version"`
	Build   string `json:"build"`
	Runs    []Run  `json:"runs"`
}

func Load(path string) ([]Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []Case
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cases); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("fixture file must contain one JSON array")
	}
	names := map[string]bool{}
	for _, c := range cases {
		if c.Name == "" || names[c.Name] || c.Before == nil || c.Head == nil || c.Issues == nil ||
			(c.Split != "tuning" && c.Split != "holdout") {
			return nil, errors.New("invalid or duplicate evaluation case")
		}
		names[c.Name] = true
	}
	return cases, nil
}

// Prepare writes only source inputs. Labels never enter the model workspace.
// Whole-file hunks avoid depending on an external diff executable.
func Prepare(ws *artifact.Workspace, c Case) (string, error) {
	var paths []string
	for path := range c.Before {
		paths = append(paths, path)
	}
	for path := range c.Head {
		if _, ok := c.Before[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var patch strings.Builder
	for _, path := range paths {
		old, hadOld := c.Before[path]
		next, hasNew := c.Head[path]
		if hadOld && hasNew && old == next {
			continue
		}
		if strings.ContainsAny(path, "\r\n\t\"") || strings.HasPrefix(path, "/") {
			return "", errors.New("unsupported evaluation fixture path")
		}
		oldPath, newPath := "a/"+path, "b/"+path
		oldLines, newLines := strings.Split(old, "\n"), strings.Split(next, "\n")
		oldStart, newStart := 1, 1
		if !hadOld {
			oldPath, oldLines, oldStart = "/dev/null", nil, 0
		}
		if !hasNew {
			newPath, newLines, newStart = "/dev/null", nil, 0
		}
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\n--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n",
			path, path, oldPath, newPath, oldStart, len(oldLines), newStart, len(newLines))
		for _, line := range oldLines {
			patch.WriteString("-" + line + "\n")
		}
		for _, line := range newLines {
			patch.WriteString("+" + line + "\n")
		}
	}
	input, err := json.Marshal(struct {
		Before map[string]string
		Head   map[string]string
	}{c.Before, c.Head})
	if err != nil {
		return "", err
	}
	headData, err := json.Marshal(c.Head)
	if err != nil {
		return "", err
	}
	beforeData, err := json.Marshal(c.Before)
	if err != nil {
		return "", err
	}
	headSHA, beforeSHA := review.Digest(headData), review.Digest(beforeData)
	head := &source.Snapshot{Commit: headSHA, Files: c.Head}
	before := &source.Snapshot{Commit: beforeSHA, Files: c.Before}
	valid, err := diff.ParseValidLines(strings.NewReader(patch.String()))
	if err != nil {
		return "", err
	}
	manifest := artifact.InputManifest{
		Version: 1, Repo: "evaluation/fixture", PRNumber: 1,
		HeadSHA: headSHA, TargetBaseSHA: beforeSHA, MergeBaseSHA: beforeSHA,
		DiffDigest:    review.Digest([]byte(patch.String())),
		ContextStatus: "complete", Head: artifact.ContextState{Status: "available"},
		Before: artifact.ContextState{Status: "available"},
	}
	for name, value := range map[string]any{
		artifact.FileInputManifest: manifest,
		artifact.FileSource:        head, artifact.FileSourceBefore: before,
		artifact.FileValidLines: valid, artifact.FileExistingInline: map[string]any{},
		artifact.FileExistingFeedbackJSON: artifact.TrustedFeedback{Version: 1, Status: "available", Comments: []artifact.TrustedComment{}},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		if err := ws.Write(name, data); err != nil {
			return "", err
		}
	}
	if err := ws.Write(artifact.FileHeadSHA, []byte(headSHA)); err != nil {
		return "", err
	}
	if err := ws.Write(artifact.FileDiff, []byte(patch.String())); err != nil {
		return "", err
	}
	return review.Digest(input), nil
}

type Judgment struct {
	RunID   string `json:"run_id"`
	Finding int    `json:"finding"`
	// Issue is an expected issue ID or the explicit verdict "false_positive".
	Issue       string `json:"issue"`
	AnchorValid *bool  `json:"anchor_valid"`
}

type SummaryJudgment struct {
	RunID    string `json:"run_id"`
	Accurate *bool  `json:"accurate"`
}

type Judgments struct {
	Findings  []Judgment        `json:"findings"`
	Summaries []SummaryJudgment `json:"summaries"`
}

type Metrics struct {
	Runs              int     `json:"runs"`
	FailedRuns        int     `json:"failed_runs"`
	TruePositives     int     `json:"true_positives"`
	FalsePositives    int     `json:"false_positives"`
	Missed            int     `json:"missed"`
	Duplicates        int     `json:"duplicates"`
	NegativeRuns      int     `json:"negative_runs"`
	NegativeFalseRuns int     `json:"negative_false_runs"`
	Precision         float64 `json:"precision"`
	Recall            float64 `json:"recall"`
	AnchorsAssessed   int     `json:"anchors_assessed"`
	ValidAnchors      int     `json:"valid_anchors"`
	SummariesAssessed int     `json:"summaries_assessed"`
	AccurateSummaries int     `json:"accurate_summaries"`
}

// Score requires explicit human adjudication for every output finding.
// It never guesses issue matches from word overlap or treats adoption as truth.
func Score(report Report, judgments Judgments) (Metrics, error) {
	m := Metrics{Runs: len(report.Runs)}
	if report.Version != 1 {
		return m, errors.New("unsupported evaluation report version")
	}
	byRun := map[string]map[int]string{}
	for _, j := range judgments.Findings {
		if j.Finding < 0 || j.AnchorValid == nil {
			return m, errors.New("each finding judgment needs a nonnegative index and anchor_valid")
		}
		if byRun[j.RunID] == nil {
			byRun[j.RunID] = map[int]string{}
		}
		if _, exists := byRun[j.RunID][j.Finding]; exists {
			return m, errors.New("duplicate judgment")
		}
		byRun[j.RunID][j.Finding] = j.Issue
		m.AnchorsAssessed++
		if *j.AnchorValid {
			m.ValidAnchors++
		}
	}
	summaries := map[string]bool{}
	for _, j := range judgments.Summaries {
		if _, exists := summaries[j.RunID]; exists || j.Accurate == nil {
			return m, errors.New("each summary needs one explicit accuracy judgment")
		}
		summaries[j.RunID] = *j.Accurate
	}
	seen := map[string]bool{}
	for _, run := range report.Runs {
		if run.ID == "" || seen[run.ID] {
			return m, errors.New("missing or duplicate run ID")
		}
		seen[run.ID] = true
		accurate, ok := summaries[run.ID]
		if !ok {
			return m, fmt.Errorf("run %s needs a summary accuracy judgment", run.ID)
		}
		m.SummariesAssessed++
		if accurate {
			m.AccurateSummaries++
		}
		if run.Failed {
			m.FailedRuns++
		}
		findings := append(append([]review.Comment{}, run.Review.Comments...), run.Review.SummaryFindings...)
		expected := map[string]bool{}
		for _, issue := range run.Expected {
			expected[issue.ID] = true
		}
		matched := map[string]bool{}
		falseCount := 0
		if len(byRun[run.ID]) != len(findings) {
			return m, fmt.Errorf("run %s needs one judgment per finding", run.ID)
		}
		for i := range findings {
			issue, ok := byRun[run.ID][i]
			if !ok {
				return m, errors.New("judgment references an invalid finding index")
			}
			switch {
			case issue == "false_positive":
				falseCount++
			case !expected[issue]:
				return m, errors.New("judgment references an unknown issue; adjudicate/update labels first")
			case matched[issue]:
				m.Duplicates++
				falseCount++
			default:
				matched[issue] = true
				m.TruePositives++
			}
		}
		m.FalsePositives += falseCount
		m.Missed += len(expected) - len(matched)
		if len(expected) == 0 {
			m.NegativeRuns++
			if falseCount > 0 {
				m.NegativeFalseRuns++
			}
		}
	}
	for id := range byRun {
		if !seen[id] {
			return m, errors.New("judgment references an unknown run")
		}
		for id := range summaries {
			if !seen[id] {
				return m, errors.New("summary judgment references an unknown run")
			}
		}
	}
	if count := m.TruePositives + m.FalsePositives; count > 0 {
		m.Precision = float64(m.TruePositives) / float64(count)
	}
	if count := m.TruePositives + m.Missed; count > 0 {
		m.Recall = float64(m.TruePositives) / float64(count)
	}
	return m, nil
}
