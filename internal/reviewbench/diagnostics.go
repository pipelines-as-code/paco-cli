package reviewbench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
)

type diagnostics struct {
	PR             PR                         `json:"pr"`
	Outcome        string                     `json:"outcome"`
	Reason         string                     `json:"reason,omitempty"`
	Summary        string                     `json:"summary,omitempty"`
	Backend        string                     `json:"backend,omitempty"`
	Strategy       string                     `json:"strategy"`
	PromptDigest   string                     `json:"prompt_digest"`
	ElapsedSeconds float64                    `json:"elapsed_seconds"`
	Requests       []requestSettings          `json:"requests"`
	Budget         model.BudgetSnapshot       `json:"budget"`
	Context        *artifact.InputManifest    `json:"context,omitempty"`
	Verification   *review.VerificationStatus `json:"verification,omitempty"`
}

type requestSettings struct {
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Exploration bool   `json:"exploration"`
	WebSearch   bool   `json:"web_search"`
	Structured  bool   `json:"structured"`
}

type diagnosticRun struct {
	report    diagnostics
	start     time.Time
	budget    *model.Budget
	secrets   []string
	capture   bool
	responses []capturedResponse
}

type capturedResponse struct {
	Text     string `json:"text,omitempty"`
	Withheld bool   `json:"withheld"`
}

// Observe effective requests rather than duplicating review's default settings.
type diagnosticClient struct {
	model.Client
	run *diagnosticRun
}

func (c diagnosticClient) Complete(ctx context.Context, req model.Request) (model.Result, error) {
	c.run.report.Requests = append(c.run.report.Requests, requestSettings{
		Model: req.Model, Effort: req.Effort, Exploration: req.Tools != nil,
		WebSearch: req.WebSearch, Structured: req.Schema != nil,
	})
	result, err := c.Client.Complete(ctx, req)
	if c.run.capture {
		response := capturedResponse{}
		if security.ScanSecrets(result.Text, c.run.secrets...) != "" {
			response.Withheld = true
		} else {
			response.Text = security.Scrub(result.Text, c.run.secrets...)
		}
		c.run.responses = append(c.run.responses, response)
	}
	return result, err
}

func newDiagnosticRun(cfg *Config) *diagnosticRun {
	if cfg.Review.Budget == nil {
		cfg.Review.Budget = model.NewBudget()
	}
	d := &diagnosticRun{
		start: time.Now(), budget: cfg.Review.Budget,
		capture: cfg.Responses != "", responses: []capturedResponse{},
		report: diagnostics{
			PR:      PR{Repo: cfg.Repo, PRNumber: cfg.PRNumber, Base: cfg.BaseSHA, Head: cfg.HeadSHA},
			Outcome: "complete", Strategy: "single", PromptDigest: review.PromptDigest(),
			Requests: []requestSettings{},
		},
	}
	if cfg.Review.VerifyFindings {
		d.report.Strategy = "verified"
	}
	resolve := cfg.Review.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context) (*model.Resolved, error) {
			return model.Resolve(ctx, model.Config{})
		}
	}
	cfg.Review.Resolve = func(ctx context.Context) (*model.Resolved, error) {
		backend, err := resolve(ctx)
		if err != nil {
			return nil, err
		}
		d.secrets = append(d.secrets, backend.Secrets...)
		d.report.Backend = backend.Backend
		wrapped := *backend
		wrapped.Client = diagnosticClient{Client: backend.Client, run: d}
		return &wrapped, nil
	}
	return d
}

func (d *diagnosticRun) write(path string, ws *artifact.Workspace, runErr error) error {
	d.report.ElapsedSeconds = time.Since(d.start).Seconds()
	d.report.Budget = d.budget.Snapshot()
	if runErr != nil {
		d.report.Outcome, d.report.Reason = "failed", runErr.Error()
	}
	read := func(name string, value any) error {
		data, err := ws.Read(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(data, value)
	}
	if err := read(artifact.FileInputManifest, &d.report.Context); err != nil {
		return err
	}
	if ws.Exists(artifact.FileSecurityBlock) {
		d.report.Outcome = "security_blocked"
	} else {
		var result review.Review
		if err := read(artifact.FileReview, &result); err != nil {
			return err
		}
		d.report.Summary = result.Summary
		if err := read(artifact.FileStatus, &d.report.Verification); err != nil {
			return err
		}
	}
	// Scrub JSON string values, not serialized bytes: escaped literals must
	// receive the same redaction as plain strings.
	data, err := json.Marshal(d.report)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return writeJSON(path, scrubDiagnostic(value, d.secrets))
}

func scrubDiagnostic(value any, secrets []string) any {
	switch v := value.(type) {
	case string:
		return security.Scrub(v, secrets...)
	case map[string]any:
		for key, item := range v {
			v[key] = scrubDiagnostic(item, secrets)
		}
	case []any:
		for i, item := range v {
			v[i] = scrubDiagnostic(item, secrets)
		}
	}
	return value
}
