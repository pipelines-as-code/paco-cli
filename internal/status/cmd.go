package status

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
	"github.com/spf13/cobra"
)

type CommandOptions struct {
	Repo      string
	PRNumber  int
	CommentID string
	RunID     string
	State     string
	Labels    bool
	Prefix    string
	LogWriter io.Writer
	// GitHub is the API client; nil builds one from the environment.
	GitHub *ghclient.Client
}

// Command is run by the pipeline's finally task when the review task did not
// succeed, covering crashes and timeouts that paco itself never sees.
func Command() *cobra.Command {
	var opts CommandOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Mark a Paco run as failed on the pull request",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunCommand(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Repo, "repo", "", "GitHub repository (owner/name)")
	cmd.Flags().IntVar(&opts.PRNumber, "pr", 0, "Pull request number")
	cmd.Flags().StringVar(&opts.CommentID, "comment-id", "", "Trigger comment ID (optional)")
	cmd.Flags().StringVar(&opts.RunID, "run-id", "", "Run that fetched the diff, e.g. the PipelineRun name")
	cmd.Flags().StringVar(&opts.State, "state", "", "State to report; only \"failed\" is supported")
	cmd.Flags().BoolVar(&opts.Labels, "status-labels", true, "Set a status label on the pull request")
	cmd.Flags().StringVar(&opts.Prefix, "status-label-prefix", DefaultPrefix, "Prefix of the status labels")
	for _, name := range []string{"repo", "pr", "run-id", "state"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// RunCommand returns an error only for invalid flags, so a reporting failure
// never turns a pipeline red on its own.
func RunCommand(ctx context.Context, opts CommandOptions) error {
	if opts.State != string(Failed) {
		return fmt.Errorf("unsupported --state %q: only %q is supported", opts.State, Failed)
	}
	if !ValidRunID(opts.RunID) {
		return fmt.Errorf("invalid --run-id %q", opts.RunID)
	}
	if opts.PRNumber <= 0 {
		return errors.New("--pr must be a positive number")
	}
	repo, err := ghclient.ParseRepo(opts.Repo)
	if err != nil {
		return err
	}
	gh := opts.GitHub
	if gh == nil {
		if gh, err = ghclient.FromEnv(); err != nil {
			progress.New(opts.LogWriter, nil).Line("Warning: could not configure GitHub access: %s", err.Error())
			return nil
		}
	}
	rep := New(ctx, Config{
		GitHub: gh, Repo: repo, PR: opts.PRNumber, CommentID: opts.CommentID, RunID: opts.RunID,
		Labels: opts.Labels, Prefix: opts.Prefix, Log: progress.New(opts.LogWriter, []string{gh.Token()}),
	})
	if rep != nil {
		rep.Fail(ctx)
	}
	return nil
}
