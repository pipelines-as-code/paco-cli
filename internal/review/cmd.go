package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/command"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/spf13/cobra"
)

const openCodeTimeout = 900 * time.Second

type Options struct {
	Workspace      string
	Model          string
	TriggerComment string
	Runner         command.Runner
}

func Command() *cobra.Command {
	var opts Options

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run AI review on a PR diff",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Runner = &command.ExecRunner{}
			opts.TriggerComment = os.Getenv("TRIGGER_COMMENT")
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.Workspace, "workspace", ".", "Workspace directory for artifacts")
	cmd.Flags().StringVar(&opts.Model, "model", "google-vertex-anthropic/claude-sonnet-5@default", "Model to use")

	return cmd
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

	// Check diff exists
	diffData, err := ws.Read(artifact.FileDiff)
	if err != nil || len(diffData) == 0 {
		return writeFail("No reviewable changes found in this diff.")
	}

	// Validate credentials
	credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credFile == "" {
		return writeFail("Paco: GOOGLE_APPLICATION_CREDENTIALS not set.")
	}
	credData, err := os.ReadFile(credFile)
	if err != nil {
		return writeFail("Paco: the Vertex AI service-account credentials are missing or invalid.")
	}
	var creds struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal(credData, &creds); err != nil || creds.ClientEmail == "" || creds.PrivateKey == "" {
		return writeFail("Paco: the Vertex AI service-account credentials are missing or invalid.")
	}

	vertexProject := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if vertexProject == "" {
		vertexProject = creds.ProjectID
	}
	if vertexProject == "" {
		return writeFail("Paco: no Vertex AI project was configured or found in the service-account credentials.")
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
	prompt := BuildPrompt(mode, string(diffData), string(feedback), string(reviewRules))

	// OpenCode config
	openCodeConfig := `{
  "permission": "deny",
  "share": "disabled",
  "autoupdate": false,
  "agent": {
    "paco-reviewer": {
      "description": "Returns a JSON-only pull request review.",
      "mode": "primary",
      "prompt": "You are a non-agentic pull request reviewer. Tools are unavailable and must not be mentioned, requested, or used. Analyze only the supplied prompt and return its requested JSON object with no prose or markdown.",
      "permission": "deny"
    }
  }
}`

	// Run opencode with sanitized environment
	vertexLocation := os.Getenv("VERTEX_LOCATION")
	if vertexLocation == "" {
		vertexLocation = "global"
	}

	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp/opencode-home"
	}
	path := os.Getenv("PATH")

	env := []string{
		"HOME=" + home,
		"PATH=" + path,
		"TERM=dumb",
		"NO_COLOR=1",
		"GOOGLE_APPLICATION_CREDENTIALS=" + credFile,
		"GOOGLE_CLOUD_PROJECT=" + vertexProject,
		"VERTEX_LOCATION=" + vertexLocation,
		"OPENCODE_CONFIG_CONTENT=" + openCodeConfig,
	}

	fmt.Println("Starting opencode review...")
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

	runCtx, cancel := context.WithTimeout(ctx, openCodeTimeout)
	defer cancel()

	result, err := opts.Runner.Run(runCtx, "opencode", []string{
		"run",
		"--agent", "paco-reviewer",
		"--model", opts.Model,
		"--variant", "minimal",
	}, env, []byte(prompt))

	close(stopHeartbeat)
	elapsed := time.Since(startedAt)

	if err != nil || result.ExitCode != 0 {
		stderr := security.Redact(string(result.Stderr))
		if len(stderr) > 4000 {
			stderr = stderr[:4000]
		}
		fmt.Printf("--- opencode stderr (scrubbed) ---\n%s\n", stderr)
		return writeFail("Paco: the Gemini/OpenCode backend exited with an error; check the PipelineRun logs.")
	}
	fmt.Printf("OpenCode completed in %ds; validating review output\n", int(elapsed.Seconds()))

	rawOutput := string(result.Stdout)

	// Secret scan model output
	if reason := security.ScanSecrets(rawOutput, creds.ClientEmail); reason != "" {
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
		scrubbedOutput := security.Redact(rawOutput)
		if len(scrubbedOutput) > 4000 {
			scrubbedOutput = scrubbedOutput[:4000]
		}
		fmt.Printf("--- unparsable opencode output (scrubbed) ---\n%s\n", scrubbedOutput)
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
