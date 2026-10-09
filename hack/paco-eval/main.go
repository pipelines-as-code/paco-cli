package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/eval"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/security"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	fixtures := flag.String("fixtures", "internal/review/testdata/eval/cases.json", "Original synthetic fixture corpus")
	selected := flag.String("cases", "", "Required comma-separated case names for a live run")
	output := flag.String("output", "", "Required report file; must not already exist")
	live := flag.Bool("live", false, "Explicitly authorize model calls; never posts to GitHub")
	list := flag.Bool("list", false, "List available case names without model calls")
	strategy := flag.String("strategy", "verified", "single or verified")
	repeat := flag.Int("repeat", 1, "Repetitions per selected case")
	modelID := flag.String("model", "", "Model ID (backend default when empty)")
	effort := flag.String("reasoning-effort", "low", "Reasoning effort")
	noStructuredOutput := flag.Bool("no-structured-output", true, "Omit the API response schema; validate model output locally")
	investigationUpdates := flag.Bool("investigation-updates", true, "Emit brief public investigation updates; set false for a baseline")
	inventory := flag.Bool("inventory", true, "Give the reviewer the Go change inventory; set false for a baseline")
	inputLimit := flag.Int64("max-input-tokens", 0, "Required cumulative input-token stop threshold (provider-reported, may overshoot one request)")
	outputLimit := flag.Int64("max-output-tokens", 0, "Required cumulative output-token cap")
	scorePath := flag.String("score", "", "Score a saved report without model calls")
	judgmentsPath := flag.String("judgments", "", "Human judgment JSON for --score")
	flag.Parse()
	if *scorePath != "" {
		if *live || *judgmentsPath == "" {
			return errors.New("--score needs --judgments and cannot be used with --live")
		}
		var report eval.Report
		var judgments eval.Judgments
		for path, value := range map[string]any{*scorePath: &report, *judgmentsPath: &judgments} {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, value); err != nil {
				return err
			}
		}
		metrics, err := eval.Score(report, judgments)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(metrics)
	}
	cases, err := eval.Load(*fixtures)
	if err != nil {
		return err
	}
	if *list {
		for _, c := range cases {
			fmt.Printf("%s\t%s\n", c.Name, c.Split)
		}
		return nil
	}
	if !*live || *selected == "" || *output == "" || *inputLimit <= 0 || *outputLimit <= 0 || *repeat < 1 {
		return errors.New("live runs require --live, --cases, --output, positive --max-input-tokens and --max-output-tokens, and --repeat >= 1")
	}
	if *strategy != "single" && *strategy != "verified" {
		return errors.New("--strategy must be single or verified")
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(*selected, ",") {
		wanted[name] = true
	}
	var chosen []eval.Case
	for _, c := range cases {
		if wanted[c.Name] {
			chosen = append(chosen, c)
			delete(wanted, c.Name)
		}
	}
	if len(wanted) != 0 {
		return errors.New("--cases contains an unknown case name")
	}
	reportFile, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = reportFile.Close() }()
	report := eval.Report{Version: 1, Build: buildRevision(), Runs: []eval.Run{}}
	save := func() error {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := reportFile.Truncate(0); err != nil {
			return err
		}
		if _, err := reportFile.Seek(0, 0); err != nil {
			return err
		}
		if _, err := reportFile.Write(data); err != nil {
			return err
		}
		return reportFile.Sync()
	}
	var inputUsed, outputUsed int64
	for _, c := range chosen {
		for n := 0; n < *repeat; n++ {
			if inputUsed >= *inputLimit || outputUsed >= *outputLimit {
				return errors.New("evaluation token allowance exhausted; completed results are saved")
			}
			budget, err := model.NewBudgetWithTokenLimits(model.TokenLimits{
				MaxInputTokens: *inputLimit - inputUsed, MaxOutputTokens: *outputLimit - outputUsed,
			})
			if err != nil {
				return err
			}
			result, err := runCase(c, n, *strategy, *modelID, *effort, *noStructuredOutput, *inventory, *investigationUpdates, budget)
			if err != nil {
				return err
			}
			inputUsed += result.Usage.InputTokens
			outputUsed += result.Usage.OutputTokens
			report.Runs = append(report.Runs, result)
			if err := save(); err != nil {
				return err
			}
		}
	}
	return nil
}

func runCase(c eval.Case, repetition int, strategy, modelID, effort string, noStructuredOutput, inventory, investigationUpdates bool, budget *model.Budget) (eval.Run, error) {
	result := eval.Run{
		ID:   fmt.Sprintf("%s/%s/%d", c.Name, strategy, repetition+1),
		Case: c.Name, Split: c.Split, Strategy: strategy, Model: modelID, Effort: effort, Expected: c.Issues,
		PromptDigest:         review.PromptDigest(),
		StructuredOutput:     !noStructuredOutput,
		Inventory:            inventory,
		InvestigationUpdates: investigationUpdates,
	}
	dir, err := os.MkdirTemp("", "paco-eval-")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ws := &artifact.Workspace{Dir: dir}
	result.InputDigest, err = eval.Prepare(ws, c)
	if err != nil {
		return result, err
	}
	start := time.Now()
	// Resolve inside Run's deadline, including credential acquisition.
	var secrets []string
	resolve := func(ctx context.Context) (*model.Resolved, error) {
		backend, err := model.Resolve(ctx, model.Config{})
		if err == nil {
			secrets = backend.Secrets
			if result.Model == "" {
				result.Model = backend.DefaultModel
			}
		}
		return backend, err
	}
	err = review.Run(context.Background(), review.Options{
		Workspace: dir, VerifyFindings: strategy == "verified", Budget: budget,
		Model: modelID, ReasoningEffort: effort, NoStructuredOutput: noStructuredOutput,
		WebSearch: false, NoInventory: !inventory, NoInvestigationUpdates: !investigationUpdates, Resolve: resolve,
	})
	result.Milliseconds = time.Since(start).Milliseconds()
	result.Usage = budget.Snapshot().Usage
	result.Failed = err != nil || ws.Exists(artifact.FileFailed) || ws.Exists(artifact.FileSecurityBlock)
	if err != nil {
		result.Error = security.Scrub(err.Error(), secrets...)
	}
	data, readErr := ws.Read(artifact.FileReview)
	if readErr != nil {
		result.Failed = true
		result.Error = "review output unavailable"
		return result, nil
	}
	if err := json.Unmarshal(data, &result.Review); err != nil {
		return result, err
	}
	if result.Review.Verified {
		status, err := review.ReadStatus(ws, data)
		if err != nil {
			return result, err
		}
		result.Status = status
	}
	return result, nil
}

func buildRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision string
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				revision += "-dirty"
			}
		}
		if revision != "" {
			return revision
		}
	}
	return "unknown"
}
