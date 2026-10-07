// Command paco-reviewbench runs one paco review under the ReviewBench agent
// contract (https://github.com/review-bench/ReviewBench/blob/main/AGENT_CONTRACT.md).
// It never calls the GitHub API and never posts anything.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/review"
	"github.com/pipelines-as-code/paco-cli/internal/reviewbench"
)

const defaultTimeout = 780 * time.Second

var sha = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "paco-reviewbench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}
	diff, err := os.ReadFile(cfg.diffPath)
	if err != nil {
		return fmt.Errorf("reading diff: %w", err)
	}
	cfg.Diff = string(diff)
	return reviewbench.Run(ctx, cfg.Config)
}

type config struct {
	reviewbench.Config
	diffPath string
}

func configFromEnv() (config, error) {
	env := func(name, fallback string) string {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
		return fallback
	}
	cfg := config{diffPath: env("RB_DIFF", "/work/pr/diff.patch")}
	cfg.Repo = env("RB_NWO", "")
	cfg.BaseSHA = env("RB_BASE", "")
	cfg.HeadSHA = env("RB_HEAD", "")
	cfg.RepoDir = env("RB_REPO", "/work/repo")
	cfg.Agent = env("RB_AGENT", "paco")
	cfg.Out = env("RB_OUT", "/work/out/findings.json")
	cfg.Diagnostics = env("RB_DIAGNOSTICS", "")
	cfg.Responses = env("RB_CAPTURE_RESPONSES", "")
	pr, err := strconv.Atoi(env("RB_PR_NUMBER", ""))
	if err != nil || pr < 1 {
		return cfg, errors.New("RB_PR_NUMBER must be a positive integer")
	}
	cfg.PRNumber = pr
	if strings.Count(cfg.Repo, "/") != 1 || !sha.MatchString(cfg.BaseSHA) || !sha.MatchString(cfg.HeadSHA) {
		return cfg, errors.New("RB_NWO must be owner/name and RB_BASE/RB_HEAD full commit SHAs")
	}

	strategy := env("RB_CONFIG_STRATEGY", "single")
	if strategy != "single" && strategy != "verified" {
		return cfg, fmt.Errorf("RB_CONFIG_STRATEGY must be single or verified, not %q", strategy)
	}
	boolean := func(name string, fallback bool) (bool, error) {
		value := env(name, "")
		if value == "" {
			return fallback, nil
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("%s must be a boolean, not %q", name, value)
		}
		return parsed, nil
	}
	webSearch, err := boolean("RB_CONFIG_WEB_SEARCH", true)
	if err != nil {
		return cfg, err
	}
	exploration, err := boolean("RB_CONFIG_EXPLORATION", true)
	if err != nil {
		return cfg, err
	}
	timeout := defaultTimeout
	if value := env("RB_CONFIG_TIMEOUT", ""); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 {
			return cfg, fmt.Errorf("RB_CONFIG_TIMEOUT must be a positive number of seconds, not %q", value)
		}
		timeout = time.Duration(seconds) * time.Second
	}
	cfg.Review = review.Options{
		Model:              env("RB_CONFIG_MODEL", ""),
		ReasoningEffort:    env("RB_CONFIG_EFFORT", ""),
		NoStructuredOutput: true,
		NoExploration:      !exploration,
		WebSearch:          webSearch,
		VerifyFindings:     strategy == "verified",
		Timeout:            timeout,
	}
	// ReviewBench flags configuration labels whose value never appears in the output.
	fmt.Printf("paco-reviewbench settings: model=%s effort=%s strategy=%s web_search=%s exploration=%s timeout=%s\n",
		env("RB_CONFIG_MODEL", "default"), env("RB_CONFIG_EFFORT", "default"), strategy,
		env("RB_CONFIG_WEB_SEARCH", strconv.FormatBool(webSearch)),
		env("RB_CONFIG_EXPLORATION", strconv.FormatBool(exploration)),
		env("RB_CONFIG_TIMEOUT", strconv.Itoa(int(timeout.Seconds()))))
	return cfg, nil
}
