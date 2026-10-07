package reviewbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/review"
)

type Config struct {
	Input
	Agent       string
	Out         string
	Diagnostics string
	Responses   string
	// Review carries model settings; Run sets its Workspace.
	Review review.Options
}

// Run reviews one pull request and writes the findings file. Skipped changes
// and security-blocked output produce an empty findings list, matching what
// paco would publish. Review failures return an error and write nothing, so
// the harness can retry.
func Run(ctx context.Context, cfg Config) (runErr error) {
	if cfg.Responses != "" && cfg.Diagnostics == "" {
		return errors.New("response capture requires diagnostics")
	}
	seen := map[string]bool{}
	for _, path := range []string{cfg.Out, cfg.Diagnostics, cfg.Responses} {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if seen[abs] {
			return errors.New("findings, diagnostics and response capture need separate paths")
		}
		seen[abs] = true
	}
	dir, err := os.MkdirTemp("", "paco-reviewbench-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ws := &artifact.Workspace{Dir: dir}
	var diagnostics *diagnosticRun
	if cfg.Diagnostics != "" {
		diagnostics = newDiagnosticRun(&cfg)
		defer func() {
			if cfg.Responses != "" {
				if err := writeJSON(cfg.Responses, diagnostics.responses); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("writing response capture: %w", err))
				}
			}
			if err := diagnostics.write(cfg.Diagnostics, ws, runErr); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("writing diagnostics: %w", err))
			}
		}()
	}

	out := Output{
		PR:       PR{Repo: "https://github.com/" + cfg.Repo, PRNumber: cfg.PRNumber, Base: cfg.BaseSHA, Head: cfg.HeadSHA},
		Agent:    cfg.Agent,
		Findings: []Finding{},
	}
	skip, parsed, err := Prepare(ws, cfg.Input)
	if err != nil {
		return err
	}
	if skip != "" {
		if diagnostics != nil {
			diagnostics.report.Outcome = "skipped"
			diagnostics.report.Reason = skip
		}
		fmt.Println("Skipping review:", skip)
		return writeJSON(cfg.Out, out)
	}

	opts := cfg.Review
	opts.Workspace = dir
	if err := review.Run(ctx, opts); err != nil {
		return err
	}
	data, readErr := ws.Read(artifact.FileReview)
	if ws.Exists(artifact.FileFailed) {
		var failed review.Review
		_ = json.Unmarshal(data, &failed)
		return errors.New(strings.TrimSpace("review failed: " + failed.Summary))
	}
	if ws.Exists(artifact.FileSecurityBlock) {
		fmt.Println("Review withheld by the security filter; writing no findings")
		return writeJSON(cfg.Out, out)
	}
	if readErr != nil {
		return readErr
	}
	var result review.Review
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	out.Findings = Findings(result, parsed, cfg.Agent)
	fmt.Printf("Writing %d finding(s) to %s\n", len(out.Findings), cfg.Out)
	return writeJSON(cfg.Out, out)
}
