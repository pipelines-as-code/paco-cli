package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/pipelines-as-code/paco-cli/internal/toolchain"
	"github.com/spf13/cobra"
)

const (
	reviewTimeout   = 900 * time.Second
	maxOutputTokens = 16384
	defaultEffort   = "low"
	maxLogBytes     = 4000
	systemPrompt    = "You are a non-agentic pull request reviewer. Tools are unavailable and must not be mentioned, requested, or used. Analyze only the supplied prompt and return its requested JSON object with no prose or markdown."
)

type Options struct {
	Workspace          string
	Model              string
	ReasoningEffort    string
	TriggerComment     string
	NoStructuredOutput bool
	// Resolve builds the model client; nil resolves it from the environment.
	Resolve func(ctx context.Context) (*model.Resolved, error)
}

var validReasoningEfforts = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

func normalizeReasoningEffort(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return defaultEffort, nil
	}
	if v == "none" {
		return "", nil
	}
	if !validReasoningEfforts[v] {
		return "", fmt.Errorf("invalid --reasoning-effort %q: must be one of none, low, medium, high, xhigh, max", v)
	}
	return v, nil
}

func Command() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run AI review on a PR diff",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.TriggerComment = os.Getenv("TRIGGER_COMMENT")
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Workspace, "workspace", ".", "Workspace directory for artifacts")
	cmd.Flags().StringVar(&opts.Model, "model", "",
		"Claude model id (default \""+model.DefaultVertexModel+"\" on Vertex AI, \""+model.DefaultAnthropicModel+"\" on the Anthropic API)")
	cmd.Flags().StringVar(&opts.ReasoningEffort, "reasoning-effort", "",
		"Reasoning effort: none (omit), low, medium, high, xhigh, or max (default \""+defaultEffort+"\")")
	cmd.Flags().BoolVar(&opts.NoStructuredOutput, "no-structured-output", false,
		"Omit the response JSON schema; still request JSON and validate the model output")

	return cmd
}

// scrubber removes credential patterns and known credential literals.
func scrubber(secrets []string) func(string) string {
	return func(s string) string {
		for _, lit := range secrets {
			if lit != "" {
				s = strings.ReplaceAll(s, lit, "[REDACTED]")
			}
		}
		s = security.Redact(s)
		if len(s) > maxLogBytes {
			s = s[:maxLogBytes]
		}
		return s
	}
}

func Run(ctx context.Context, opts Options) error {
	ws := &artifact.Workspace{Dir: opts.Workspace}

	writeFail := func(msg string) error {
		fmt.Fprintln(os.Stderr, msg)
		review := Review{Summary: msg, Comments: []Comment{}}
		data, _ := json.Marshal(review)
		if err := ws.Write(artifact.FileReview, data); err != nil {
			return err
		}
		return ws.Write(artifact.FileFailed, nil)
	}

	// Check for skip from diff step
	if ws.Exists(artifact.FileError) {
		errMsg, _ := ws.Read(artifact.FileError)
		return writeFail(strings.TrimSpace(string(errMsg)))
	}

	effort, err := normalizeReasoningEffort(opts.ReasoningEffort)
	if err != nil {
		return writeFail("Paco: " + err.Error())
	}

	// Check diff exists
	diffData, err := ws.Read(artifact.FileDiff)
	if err != nil || len(diffData) == 0 {
		return writeFail("No reviewable changes found in this diff.")
	}

	runCtx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()

	resolve := opts.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context) (*model.Resolved, error) { return model.Resolve(ctx, model.Config{}) }
	}
	backend, err := resolve(runCtx)
	if err != nil {
		return writeFail("Paco: " + security.Redact(err.Error()) + ".")
	}

	// Determine mode
	mode := "review"
	firstLine := strings.SplitN(opts.TriggerComment, "\n", 2)[0]
	fields := strings.Fields(firstLine)
	if len(fields) >= 2 && strings.ToLower(fields[1]) == "summary" {
		mode = "summary"
	}
	fmt.Printf("Running Paco in %s mode\n", mode)
	if err := ws.Write(artifact.FileMode, []byte(mode+"\n")); err != nil {
		return err
	}

	// Build prompt
	feedback, _ := ws.Read(artifact.FileExistingFeedback)
	reviewRules, _ := ws.Read(artifact.FileReviewRules)
	toolchainData, _ := ws.Read(artifact.FileToolchains)
	prompt := BuildPrompt(mode, string(diffData), string(feedback), string(reviewRules), toolchain.Parse(toolchainData))

	scrub := scrubber(backend.Secrets)
	modelID := opts.Model
	if modelID == "" {
		modelID = backend.DefaultModel
	}

	schema := reviewSchema
	if opts.NoStructuredOutput {
		schema = nil
	}
	effortLabel := effort
	if effortLabel == "" {
		effortLabel = "omitted"
	}
	fmt.Printf("Starting model review (backend=%s, model=%s, effort=%s, structured-output=%t)...\n",
		backend.Backend, modelID, effortLabel, schema != nil)
	startedAt := time.Now()

	stopHeartbeat := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Printf("Paco review still running (%ds elapsed)\n", int(time.Since(startedAt).Seconds()))
			case <-stopHeartbeat:
				return
			}
		}
	}()

	result, err := backend.Client.Complete(runCtx, model.Request{
		System:    systemPrompt,
		Prompt:    prompt,
		Model:     modelID,
		Effort:    effort,
		Schema:    schema,
		MaxTokens: maxOutputTokens,
	})

	close(stopHeartbeat)
	elapsed := time.Since(startedAt)

	if err != nil {
		fmt.Printf("--- model error (scrubbed) ---\n%s\n", scrub(err.Error()))
		var incomplete *model.IncompleteError
		switch {
		case errors.As(err, &incomplete):
			return writeFail("Paco: " + incomplete.Error() + ".")
		case errors.Is(err, context.DeadlineExceeded):
			return writeFail(fmt.Sprintf("Paco: the model review timed out after %ds.", int(reviewTimeout.Seconds())))
		default:
			return writeFail("Paco: the model backend returned an error; check the PipelineRun logs.")
		}
	}
	fmt.Printf("Model completed in %ds; validating review output\n", int(elapsed.Seconds()))

	rawOutput := result.Text

	// Secret scan model output
	if reason := security.ScanSecrets(rawOutput, backend.Secrets...); reason != "" {
		fmt.Printf("Security filter tripped: %s; withholding review.\n", reason)
		if err := ws.Write(artifact.FileSecurityBlock, []byte(reason+"\n")); err != nil {
			return err
		}
		emptyReview, _ := json.Marshal(Review{Summary: "", Comments: []Comment{}})
		return ws.Write(artifact.FileReview, emptyReview)
	}

	// Extract JSON review from model output
	review, err := ExtractReview(rawOutput)
	if err != nil || review == nil {
		fmt.Printf("--- unparsable model output (scrubbed) ---\n%s\n", scrub(rawOutput))
		return writeFail("Paco: the model returned output that could not be parsed as a review.")
	}

	// Normalize
	normalized := Normalize(review)
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	if err := ws.Write(artifact.FileReview, data); err != nil {
		return err
	}

	fmt.Printf("Paco generated %d normalized finding(s) in %s mode\n", len(normalized.Comments), mode)
	return nil
}
