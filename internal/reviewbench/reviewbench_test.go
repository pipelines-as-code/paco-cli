package reviewbench_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/reviewbench"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

const (
	baseSHA = "1111111111111111111111111111111111111111"
	headSHA = "2222222222222222222222222222222222222222"
	oldDiv  = "func div(a, b int) int {\n\tif b == 0 { return 0 }\n\treturn a / b\n}\n"
	newDiv  = "func div(a, b int) int {\n\tlog.Print(a)\n\treturn a / b\n}\n"
	divDiff = "diff --git a/div.go b/div.go\n--- a/div.go\n+++ b/div.go\n@@ -1,4 +1,4 @@\n" +
		" func div(a, b int) int {\n-\tif b == 0 { return 0 }\n+\tlog.Print(a)\n \treturn a / b\n }\n"
)

type scripted struct{ responses []string }

func (s *scripted) Complete(context.Context, model.Request) (model.Result, error) {
	if len(s.responses) == 0 {
		return model.Result{}, errors.New("unexpected model request")
	}
	text := s.responses[0]
	s.responses = s.responses[1:]
	return model.Result{Text: text}, nil
}

func config(t *testing.T, diff string, responses ...string) reviewbench.Config {
	t.Helper()
	repo := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "div.go"), []byte(newDiv), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "other.go"), []byte("package x\n"), 0o600))
	client := &scripted{responses: responses}
	return reviewbench.Config{
		Input: reviewbench.Input{
			Repo: "owner/repo", PRNumber: 7, BaseSHA: baseSHA, HeadSHA: headSHA, RepoDir: repo, Diff: diff,
		},
		Agent: "paco-test",
		Out:   filepath.Join(t.TempDir(), "findings.json"),
		Review: review.Options{
			NoStructuredOutput: true,
			Resolve: func(context.Context) (*model.Resolved, error) {
				return &model.Resolved{Client: client, Backend: "fake", DefaultModel: "fake-model"}, nil
			},
		},
	}
}

func jsonText(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	assert.NilError(t, err)
	return string(data)
}

func readOutput(t *testing.T, path string) reviewbench.Output {
	t.Helper()
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	var out reviewbench.Output
	assert.NilError(t, json.Unmarshal(data, &out))
	return out
}

func TestPrepareRebuildsBeforeSnapshot(t *testing.T) {
	cfg := config(t, divDiff)
	ws := &artifact.Workspace{Dir: t.TempDir()}
	skip, _, err := reviewbench.Prepare(ws, cfg.Input)
	assert.NilError(t, err)
	assert.Equal(t, skip, "")

	var manifest artifact.InputManifest
	data, err := ws.Read(artifact.FileInputManifest)
	assert.NilError(t, err)
	assert.NilError(t, json.Unmarshal(data, &manifest))
	assert.Equal(t, manifest.ContextStatus, "complete")
	assert.Equal(t, manifest.Head.Status, "available")
	assert.Equal(t, manifest.Before.Status, "available")
	assert.Equal(t, manifest.MergeBaseSHA, baseSHA)
	assert.Equal(t, manifest.DiffDigest, review.Digest([]byte(divDiff)))

	data, err = ws.Read(artifact.FileSourceBefore)
	assert.NilError(t, err)
	before, err := source.Decode(data, baseSHA)
	assert.NilError(t, err)
	assert.DeepEqual(t, before.Files, map[string]string{"div.go": oldDiv, "other.go": "package x\n"})
}

func TestRunSinglePass(t *testing.T) {
	cfg := config(t, divDiff, jsonText(t, review.Review{
		Summary: "Logs input.", ReviewScore: review.ReviewScore{Rating: 2, Reason: "Small."},
		Comments: []review.Comment{{Path: "div.go", Line: 2, Severity: "high", Body: "The zero-divisor guard was removed."}},
	}))
	assert.NilError(t, reviewbench.Run(context.Background(), cfg))

	out := readOutput(t, cfg.Out)
	assert.DeepEqual(t, out, reviewbench.Output{
		PR:    reviewbench.PR{Repo: "https://github.com/owner/repo", PRNumber: 7, Base: baseSHA, Head: headSHA},
		Agent: "paco-test",
		Findings: []reviewbench.Finding{{
			File: "div.go", StartLine: 2, EndLine: 2, Message: "The zero-divisor guard was removed.", Producer: "paco-test",
		}},
	})
}

func TestRunVerifiedAnchorsDeletedLines(t *testing.T) {
	evidence := []map[string]any{{"revision": "before", "path": "div.go", "start": 2, "end": 2, "quote": "\tif b == 0 { return 0 }"}}
	discovery := map[string]any{
		"summary": "Replaces the zero guard with logging.", "review_score": map[string]any{"rating": 2, "reason": "Small."},
		"security_sensitive": false, "overflow": false,
		"candidates": []map[string]any{{
			"id": "guard", "path": "div.go", "line": 2, "side": "before", "severity": "high",
			"claim": "Division by zero is no longer guarded", "trigger": "When b is zero.",
			"impact": "The process panics.", "remedy": "Restore the guard.", "evidence": evidence,
		}},
	}
	verdict := map[string]any{
		"summary":   "Replaces the zero guard with logging.",
		"decisions": []map[string]any{{"id": "guard", "outcome": "accept", "reason": "Guard removed.", "duplicate_of": "", "severity": "high", "evidence": evidence}},
	}
	cfg := config(t, divDiff, jsonText(t, discovery), jsonText(t, verdict))
	cfg.Review.VerifyFindings = true
	cfg.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
	assert.NilError(t, reviewbench.Run(context.Background(), cfg))
	report := readDiagnostics(t, cfg.Diagnostics)
	assert.Equal(t, report.Verification.Accepted, 1)
	assert.Equal(t, len(report.Requests), 2)

	out := readOutput(t, cfg.Out)
	assert.Equal(t, len(out.Findings), 1)
	finding := out.Findings[0]
	assert.Equal(t, finding.File, "div.go")
	assert.Equal(t, finding.StartLine, 2)
	assert.Equal(t, finding.EndLine, 2)
	assert.Assert(t, strings.Contains(finding.Message, "Division by zero is no longer guarded"))
}

type diagnosticReport struct {
	Outcome      string
	Summary      string
	Requests     []struct{ Model, Effort string }
	Budget       model.BudgetSnapshot
	Verification *review.VerificationStatus
}

func readDiagnostics(t *testing.T, path string) diagnosticReport {
	t.Helper()
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	var report diagnosticReport
	assert.NilError(t, json.Unmarshal(data, &report))
	assert.Assert(t, !strings.Contains(string(data), oldDiv))
	return report
}

func TestDiagnosticsOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name, diff, response, outcome string
		failed                        bool
	}{
		{"complete", divDiff, `{"summary":"Logs input.","comments":[]}`, "complete", false},
		{"skipped", "", "", "skipped", false},
		{"failed", divDiff, "not JSON", "failed", true},
		{"blocked", divDiff, `{"summary":"ghp_abcdefghijklmnopqrstuvwxyz","comments":[]}`, "security_blocked", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config(t, tt.diff, tt.response)
			cfg.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
			err := reviewbench.Run(context.Background(), cfg)
			assert.Equal(t, err != nil, tt.failed)
			report := readDiagnostics(t, cfg.Diagnostics)
			assert.Equal(t, report.Outcome, tt.outcome)
			if tt.outcome == "security_blocked" {
				assert.Equal(t, report.Summary, "")
				assert.Assert(t, report.Verification == nil)
			}
			data, err := os.ReadFile(cfg.Diagnostics)
			assert.NilError(t, err)
			assert.Assert(t, !strings.Contains(string(data), "ghp_abcdefghijklmnopqrstuvwxyz"))
		})
	}
}

func TestDiagnosticsWriteFailure(t *testing.T) {
	cfg := config(t, divDiff, "not JSON")
	cfg.Diagnostics = filepath.Join(t.TempDir(), "missing", "diagnostics.json")
	err := reviewbench.Run(context.Background(), cfg)
	assert.ErrorContains(t, err, "review failed")
	assert.ErrorContains(t, err, "writing diagnostics")

	cfg = config(t, "")
	cfg.Diagnostics = cfg.Out
	assert.ErrorContains(t, reviewbench.Run(context.Background(), cfg), "separate paths")
}

func TestDiagnosticsVerifiedFailure(t *testing.T) {
	cfg := config(t, divDiff, "not JSON")
	cfg.Review.VerifyFindings = true
	cfg.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
	assert.ErrorContains(t, reviewbench.Run(context.Background(), cfg), "review failed")
	report := readDiagnostics(t, cfg.Diagnostics)
	assert.Equal(t, report.Outcome, "failed")
	assert.Equal(t, report.Verification.FailureReason, "discovery: response is not a JSON object")
}

func TestResponseCapture(t *testing.T) {
	for _, response := range []string{"not JSON", `{"summary":"ghp_abcdefghijklmnopqrstuvwxyz","comments":[]}`} {
		cfg := config(t, divDiff, response)
		cfg.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
		cfg.Responses = filepath.Join(t.TempDir(), "responses.json")
		err := reviewbench.Run(context.Background(), cfg)
		if response == "not JSON" {
			assert.ErrorContains(t, err, "review failed")
		} else {
			assert.NilError(t, err)
		}
		data, err := os.ReadFile(cfg.Responses)
		assert.NilError(t, err)
		var captures []struct {
			Text     string
			Withheld bool
		}
		assert.NilError(t, json.Unmarshal(data, &captures))
		assert.Equal(t, len(captures), 1)
		assert.Equal(t, captures[0].Withheld, response != "not JSON")
		assert.Assert(t, !strings.Contains(string(data), "ghp_abcdefghijklmnopqrstuvwxyz"))
		if response == "not JSON" {
			assert.Equal(t, captures[0].Text, response)
		}
	}
	cfg := config(t, "")
	cfg.Responses = filepath.Join(t.TempDir(), "responses.json")
	assert.ErrorContains(t, reviewbench.Run(context.Background(), cfg), "requires diagnostics")
	cfg.Diagnostics = cfg.Responses
	assert.ErrorContains(t, reviewbench.Run(context.Background(), cfg), "separate paths")
	cfg.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
	cfg.Responses = filepath.Join(t.TempDir(), "missing", "responses.json")
	assert.ErrorContains(t, reviewbench.Run(context.Background(), cfg), "writing response capture")
}

func TestRunSkipWritesNoFindings(t *testing.T) {
	cfg := config(t, "")
	assert.NilError(t, reviewbench.Run(context.Background(), cfg))
	info, err := os.Stat(cfg.Out)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o644))
	out := readOutput(t, cfg.Out)
	assert.Assert(t, out.Findings != nil)
	assert.Equal(t, len(out.Findings), 0)
	assert.Equal(t, out.PR.Head, headSHA)
}

func TestRunFailureWritesNothing(t *testing.T) {
	cfg := config(t, divDiff, "not a review")
	err := reviewbench.Run(context.Background(), cfg)
	assert.ErrorContains(t, err, "review failed")
	_, statErr := os.Stat(cfg.Out)
	assert.Assert(t, errors.Is(statErr, os.ErrNotExist))
}
