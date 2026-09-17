package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/command"
	"gotest.tools/v3/assert"
)

func writeFakeCredentials(t *testing.T) string {
	t.Helper()
	credFile := filepath.Join(t.TempDir(), "creds.json")
	creds := `{"client_email":"test@proj.iam.gserviceaccount.com","private_key":"fake-key","project_id":"test-proj"}`
	assert.NilError(t, os.WriteFile(credFile, []byte(creds), 0o600))
	return credFile
}

func setupFakeOpencode(t *testing.T, script string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "opencode")
	assert.NilError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

func setupWorkspaceWithDiff(t *testing.T, diff string) string {
	t.Helper()
	ws := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileDiff), []byte(diff), 0o600))
	return ws
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRunEarlyExit(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) (string, string)
		wantFailed  bool
		wantSummary string
	}{
		{
			name: "skips when error file exists",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				ws := t.TempDir()
				assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileError), []byte("skip reason"), 0o600))
				credFile := writeFakeCredentials(t)
				return ws, credFile
			},
			wantFailed:  true,
			wantSummary: "skip reason",
		},
		{
			name: "skips when diff is empty",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				ws := setupWorkspaceWithDiff(t, "")
				credFile := writeFakeCredentials(t)
				return ws, credFile
			},
			wantFailed:  true,
			wantSummary: "No reviewable changes found in this diff.",
		},
		{
			name: "fails with missing credentials env",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				ws := setupWorkspaceWithDiff(t, "some diff")
				return ws, ""
			},
			wantFailed: true,
		},
		{
			name: "fails with unreadable credentials file",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				ws := setupWorkspaceWithDiff(t, "some diff")
				credFile := filepath.Join(t.TempDir(), "nonexistent", "creds.json")
				return ws, credFile
			},
			wantFailed: true,
		},
		{
			name: "fails with invalid credentials JSON",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				ws := setupWorkspaceWithDiff(t, "some diff")
				credFile := filepath.Join(t.TempDir(), "bad.json")
				assert.NilError(t, os.WriteFile(credFile, []byte(`{"not":"valid"}`), 0o600))
				return ws, credFile
			},
			wantFailed: true,
		},
		{
			name: "fails with no project configured",
			setup: func(t *testing.T) (string, string) {
				t.Helper()
				ws := setupWorkspaceWithDiff(t, "some diff")
				credFile := filepath.Join(t.TempDir(), "creds.json")
				assert.NilError(t, os.WriteFile(credFile, []byte(`{"client_email":"a@b.com","private_key":"k"}`), 0o600))
				t.Setenv("GOOGLE_CLOUD_PROJECT", "")
				return ws, credFile
			},
			wantFailed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, credFile := tt.setup(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
			if credFile != "" {
				t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
			}

			err := Run(context.Background(), Options{Workspace: ws, Runner: &command.ExecRunner{}})
			assert.NilError(t, err)

			if tt.wantFailed {
				assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
			}
			if tt.wantSummary != "" {
				data, _ := os.ReadFile(filepath.Join(ws, artifact.FileReview))
				var review Review
				assert.NilError(t, json.Unmarshal(data, &review))
				assert.Equal(t, review.Summary, tt.wantSummary)
			}
		})
	}
}

func TestRunModeDetection(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		want    string
	}{
		{name: "default is review", comment: "", want: "review"},
		{name: "paco review command", comment: "/paco review", want: "review"},
		{name: "paco summary command", comment: "/paco summary", want: "summary"},
		{name: "multiline first line counts", comment: "/paco summary\nother text", want: "summary"},
		{name: "case insensitive", comment: "/paco Summary", want: "summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := setupWorkspaceWithDiff(t, "some diff")
			credFile := writeFakeCredentials(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
			t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
			setupFakeOpencode(t, `echo '{"summary":"ok","comments":[]}'`)

			err := Run(context.Background(), Options{
				Workspace:      ws,
				TriggerComment: tt.comment,
				Runner:         &command.ExecRunner{},
			})
			assert.NilError(t, err)

			modeData, _ := os.ReadFile(filepath.Join(ws, artifact.FileMode))
			assert.Equal(t, string(modeData), tt.want+"\n")
		})
	}
}

func TestRunModelOutput(t *testing.T) {
	tests := []struct {
		name              string
		opencodeOutput    string
		wantFailed        bool
		wantSecurityBlock bool
		wantSummary       string
		wantCommentCount  int
		wantRating        int
		wantSeverity      string
	}{
		{
			name:             "successful review with findings",
			opencodeOutput:   `{"summary":"found issues","review_score":{"rating":2,"reason":"small"},"comments":[{"path":"a.go","line":1,"severity":"high","body":"bug"}]}`,
			wantSummary:      "found issues",
			wantCommentCount: 1,
			wantRating:       2,
			wantSeverity:     "high",
		},
		{
			name:             "successful review no findings",
			opencodeOutput:   `{"summary":"looks good","comments":[]}`,
			wantSummary:      "looks good",
			wantCommentCount: 0,
		},
		{
			name:              "security block on leaked github token",
			opencodeOutput:    `{"summary":"ghp_ABCDEFghijklmnopqrstuvwx leaked","comments":[]}`,
			wantSecurityBlock: true,
		},
		{
			name:              "security block on service account literal",
			opencodeOutput:    `{"summary":"test@proj.iam.gserviceaccount.com in output","comments":[]}`,
			wantSecurityBlock: true,
		},
		{
			name:           "unparsable model output",
			opencodeOutput: "not json at all",
			wantFailed:     true,
		},
		{
			name:           "JSON without comments field",
			opencodeOutput: `{"summary":"no comments array"}`,
			wantFailed:     true,
		},
		{
			name:             "rating clamped to max 5",
			opencodeOutput:   `{"summary":"test","review_score":{"rating":99},"comments":[{"path":"a.go","line":1,"severity":"low","body":"ok"}]}`,
			wantCommentCount: 1,
			wantRating:       5,
		},
		{
			name:             "rating clamped to min 1",
			opencodeOutput:   `{"summary":"test","review_score":{"rating":-5},"comments":[{"path":"a.go","line":1,"severity":"low","body":"ok"}]}`,
			wantCommentCount: 1,
			wantRating:       1,
		},
		{
			name:             "unknown severity becomes medium",
			opencodeOutput:   `{"summary":"test","comments":[{"path":"a.go","line":1,"severity":"EXTREME","body":"issue"}]}`,
			wantCommentCount: 1,
			wantSeverity:     "medium",
		},
		{
			name:             "malformed comments dropped",
			opencodeOutput:   `{"summary":"test","comments":[{"path":"a.go","line":1,"severity":"low","body":"valid"},{"path":"","line":0,"body":"invalid"}]}`,
			wantCommentCount: 1,
		},
		{
			name:             "prose around JSON extracted correctly",
			opencodeOutput:   "Here is my review:\n\n" + `{"summary":"extracted","comments":[]}` + "\n\nDone.",
			wantSummary:      "extracted",
			wantCommentCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := setupWorkspaceWithDiff(t, "some diff content")
			credFile := writeFakeCredentials(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
			t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
			setupFakeOpencode(t, `printf '%s' '`+tt.opencodeOutput+`'`)

			err := Run(context.Background(), Options{
				Workspace: ws,
				Runner:    &command.ExecRunner{},
			})
			assert.NilError(t, err)

			if tt.wantSecurityBlock {
				assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileSecurityBlock)))
				return
			}

			if tt.wantFailed {
				assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
				return
			}

			assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileFailed)), "should not be marked failed")

			data, readErr := os.ReadFile(filepath.Join(ws, artifact.FileReview))
			assert.NilError(t, readErr)
			var review Review
			assert.NilError(t, json.Unmarshal(data, &review))

			if tt.wantSummary != "" {
				assert.Equal(t, review.Summary, tt.wantSummary)
			}
			assert.Equal(t, len(review.Comments), tt.wantCommentCount)
			if tt.wantRating > 0 {
				assert.Equal(t, review.ReviewScore.Rating, tt.wantRating)
			}
			if tt.wantSeverity != "" && len(review.Comments) > 0 {
				assert.Equal(t, review.Comments[0].Severity, tt.wantSeverity)
			}
		})
	}
}

func TestRunOpenCodeFailure(t *testing.T) {
	ws := setupWorkspaceWithDiff(t, "some diff")
	credFile := writeFakeCredentials(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
	setupFakeOpencode(t, `echo "error" >&2; exit 1`)

	err := Run(context.Background(), Options{
		Workspace: ws,
		Runner:    &command.ExecRunner{},
	})
	assert.NilError(t, err)
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
		valid bool
	}{
		{"empty falls back to default", "", defaultVariant, true},
		{"lowercase passes through", "high", "high", true},
		{"trims and lowercases", " HIGH \n", "high", true},
		{"none", "none", "none", true},
		{"minimal", "minimal", "minimal", true},
		{"xhigh", "xhigh", "xhigh", true},
		{"max", "max", "max", true},
		{"unknown word", "extreme", "", false},
		{"numeric", "3", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeReasoningEffort(tt.value)
			if !tt.valid {
				assert.ErrorContains(t, err, "invalid --reasoning-effort")
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}

// captureOpencode installs a fake opencode that records its argv and the
// config it was handed, so tests can assert what paco actually invokes.
func captureOpencode(t *testing.T, ws string) (argvPath, configPath string) {
	t.Helper()
	argvPath = filepath.Join(ws, "captured-argv.txt")
	configPath = filepath.Join(ws, "captured-config.json")
	setupFakeOpencode(t, fmt.Sprintf(
		`printf '%%s\n' "$@" > %s; printenv OPENCODE_CONFIG_CONTENT > %s; printf '{"summary":"ok","comments":[]}'`,
		argvPath, configPath,
	))
	return argvPath, configPath
}

func TestRunOpencodeInvocation(t *testing.T) {
	const model = "google-vertex-anthropic/claude-sonnet-5@default"
	tests := []struct {
		name        string
		effort      string
		wantVariant string
	}{
		{"defaults the variant when effort is unset", "", defaultVariant},
		{"passes the requested effort as a variant", "high", "high"},
		{"trims and lowercases the effort", " LOW \n", "low"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := setupWorkspaceWithDiff(t, "some diff")
			credFile := writeFakeCredentials(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
			t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
			argvPath, configPath := captureOpencode(t, ws)

			err := Run(context.Background(), Options{
				Workspace:       ws,
				Model:           model,
				ReasoningEffort: tt.effort,
				Runner:          &command.ExecRunner{},
			})
			assert.NilError(t, err)

			argvData, err := os.ReadFile(argvPath)
			assert.NilError(t, err)
			argv := strings.Fields(string(argvData))

			// --variant is not a real `opencode run` flag: 1.18.31 ignores it
			// and 2.x rejects it outright.
			for _, arg := range argv {
				assert.Assert(t, arg != "--variant", "argv must not contain --variant: %v", argv)
			}

			// A "#variant" suffix breaks model resolution on opencode 1.18.31,
			// so the model must be passed through untouched.
			modelIdx := -1
			for i, arg := range argv {
				if arg == "--model" {
					modelIdx = i
					break
				}
			}
			assert.Assert(t, modelIdx >= 0 && modelIdx+1 < len(argv), "argv missing --model: %v", argv)
			assert.Equal(t, argv[modelIdx+1], model)

			configData, err := os.ReadFile(configPath)
			assert.NilError(t, err)
			var config map[string]any
			assert.NilError(t, json.Unmarshal(configData, &config))

			agents := config["agent"].(map[string]any)
			reviewer := agents["paco-reviewer"].(map[string]any)
			assert.Equal(t, reviewer["variant"], tt.wantVariant)

			// options is an unvalidated passthrough that Anthropic ignores;
			// sending it would only look like the effort was applied.
			_, hasOptions := reviewer["options"]
			assert.Assert(t, !hasOptions, "agent config must not carry an options key")
		})
	}
}

func TestRunReasoningEffortInvalid(t *testing.T) {
	ws := setupWorkspaceWithDiff(t, "some diff")
	credFile := writeFakeCredentials(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")
	argvPath, _ := captureOpencode(t, ws)

	err := Run(context.Background(), Options{
		Workspace:       ws,
		ReasoningEffort: "turbo",
		Runner:          &command.ExecRunner{},
	})
	assert.NilError(t, err)
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
	assert.Assert(t, !fileExists(argvPath), "opencode must not run on invalid input")

	reviewData, err := os.ReadFile(filepath.Join(ws, artifact.FileReview))
	assert.NilError(t, err)
	var review Review
	assert.NilError(t, json.Unmarshal(reviewData, &review))
	assert.Assert(t, strings.Contains(review.Summary, "invalid --reasoning-effort"), "got summary %q", review.Summary)
}

func TestRunErrorFileWinsOverInvalidReasoningEffort(t *testing.T) {
	ws := setupWorkspaceWithDiff(t, "some diff")
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileError), []byte("skip reason"), 0o600))
	credFile := writeFakeCredentials(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-proj")

	err := Run(context.Background(), Options{
		Workspace:       ws,
		ReasoningEffort: "turbo",
		Runner:          &command.ExecRunner{},
	})
	assert.NilError(t, err)

	reviewData, err := os.ReadFile(filepath.Join(ws, artifact.FileReview))
	assert.NilError(t, err)
	var review Review
	assert.NilError(t, json.Unmarshal(reviewData, &review))
	assert.Equal(t, review.Summary, "skip reason")
}
