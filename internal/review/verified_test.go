package review

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"gotest.tools/v3/assert"
)

type testTransport struct{ target *url.URL }

func (t testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = t.target.Scheme, t.target.Host
	copy.URL = &u
	return http.DefaultTransport.RoundTrip(copy)
}

func reviewServer(t *testing.T, responses []string) (func(context.Context) (*model.Resolved, error), *[]map[string]any) {
	t.Helper()
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		if len(requests) > len(responses) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(name string, value any) {
			data, err := json.Marshal(value)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		}
		event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "test", "type": "message", "role": "assistant", "model": "test", "content": []any{},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 0},
		}})
		event("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": responses[len(requests)-1]},
		})
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		event("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 10},
		})
		event("message_stop", map[string]any{"type": "message_stop"})
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	assert.NilError(t, err)
	t.Setenv("ANTHROPIC_API_KEY", "test-model-key")
	return func(ctx context.Context) (*model.Resolved, error) {
		return model.Resolve(ctx, model.Config{Transport: testTransport{target}})
	}, &requests
}

const verifiedDiff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1 @@\n-if b == 0 { return 0 }\n return a / b\n"

func verifiedWorkspace(t *testing.T) string {
	t.Helper()
	ws := setupWorkspaceWithDiff(t, verifiedDiff)
	files := map[string]string{
		artifact.FileInputManifest: jsonText(t, artifact.InputManifest{
			Version: 1, Repo: "owner/repo", PRNumber: 1, HeadSHA: "head-sha",
			BaseRef: "main", TargetBaseSHA: "base-sha", MergeBaseSHA: "before-sha", DiffDigest: Digest([]byte(verifiedDiff)),
			ContextStatus: "complete", Head: artifact.ContextState{Status: "available"},
			Before: artifact.ContextState{Status: "available"},
		}),
		artifact.FileHeadSHA:              "head-sha",
		artifact.FileSource:               `{"commit":"head-sha","files":{"a.go":"return a / b"}}`,
		artifact.FileSourceBefore:         `{"commit":"before-sha","files":{"a.go":"if b == 0 { return 0 }\nreturn a / b"}}`,
		artifact.FileExistingFeedbackJSON: `{"version":1,"status":"available","comments":[]}`,
	}
	for name, data := range files {
		assert.NilError(t, os.WriteFile(filepath.Join(ws, name), []byte(data), 0o600))
	}
	return ws
}

func deletedCandidate() Candidate {
	c := testCandidate()
	c.Side = "before"
	c.Evidence = []Evidence{{Revision: "before", Path: "a.go", Start: 1, End: 1, Quote: "if b == 0 { return 0 }"}}
	return c
}

func TestVerifiedReviewHTTP(t *testing.T) {
	c := deletedCandidate()
	discovered := discovery{Summary: "Removes a division guard.", ReviewScore: ReviewScore{2, "Small change."}, Candidates: []Candidate{c}}
	tests := []struct {
		name     string
		response string
		failed   bool
		accepted int
		partial  bool
	}{
		{name: "accept deletion", accepted: 1, response: jsonText(t, verdict{
			Summary:   discovered.Summary,
			Decisions: []decision{{ID: c.ID, Outcome: "accept", Reason: "Guard is removed.", Severity: "high", Evidence: c.Evidence}},
		})},
		{name: "counterevidence rejects", response: jsonText(t, verdict{
			Summary: discovered.Summary,
			Decisions: []decision{{
				ID: c.ID, Outcome: "reject", Reason: "Unchanged source contradicts the claim.",
				Evidence: []Evidence{{Revision: "head", Path: "a.go", Start: 1, End: 1, Quote: "return a / b"}},
			}},
		})},
		{name: "unavailable evidence is partial", partial: true, response: jsonText(t, verdict{
			Summary:   discovered.Summary,
			Decisions: []decision{{ID: c.ID, Outcome: "insufficient_evidence", Reason: "Caller is unavailable.", Evidence: []Evidence{}}},
		})},
		{name: "missing decisions fails closed", failed: true, response: `{"summary":"ok","decisions":[]}`},
		{name: "malformed JSON fails closed", failed: true, response: `{`},
		{name: "credential is withheld", failed: true, response: `{"summary":"test-model-key","decisions":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := verifiedWorkspace(t)
			resolve, requests := reviewServer(t, []string{jsonText(t, discovered), tt.response})
			assert.NilError(t, Run(context.Background(), Options{
				Workspace: ws, VerifyFindings: true, Resolve: resolve,
			}))
			result := readReview(t, ws)
			assert.Assert(t, result.Verified)
			assert.Equal(t, len(result.Comments), 0)
			assert.Equal(t, len(result.SummaryFindings), tt.accepted)
			assert.Equal(t, fileExists(filepath.Join(ws, artifact.FileFailed)), tt.failed)
			assert.Equal(t, len(*requests), 2)
			for _, request := range *requests {
				assert.Assert(t, strings.Contains(jsonText(t, request["messages"]), "source_json="))
			}
			// The verifier starts a fresh conversation, rather than inheriting
			// the discoverer's assistant/tool history.
			messages, ok := (*requests)[1]["messages"].([]any)
			assert.Assert(t, ok)
			assert.Equal(t, len(messages), 1)
			data, err := os.ReadFile(filepath.Join(ws, artifact.FileReview))
			assert.NilError(t, err)
			status, err := ReadStatus(&artifact.Workspace{Dir: ws}, data)
			assert.NilError(t, err)
			assert.Equal(t, status.BaseRef, "main")
			assert.Equal(t, status.TargetBaseSHA, "base-sha")
			assert.Equal(t, status.Accepted, tt.accepted)
			assert.Equal(t, status.Usage.ModelRequests, int64(2))
			assert.Equal(t, status.Usage.OutputTokens, int64(20))
			assert.Equal(t, status.State == "partial", tt.partial)
		})
	}
}

func TestVerifiedInputAndReuse(t *testing.T) {
	tests := []struct {
		name      string
		edit      func(string)
		wantCalls int
		failed    bool
	}{
		{name: "valid empty review", wantCalls: 1},
		{name: "missing manifest", failed: true, edit: func(ws string) {
			assert.NilError(t, os.Remove(filepath.Join(ws, artifact.FileInputManifest)))
		}},
		{name: "missing base identity", failed: true, edit: func(ws string) {
			path := filepath.Join(ws, artifact.FileInputManifest)
			data, err := os.ReadFile(path)
			assert.NilError(t, err)
			data = []byte(strings.Replace(string(data), `"base_ref":"main"`, `"base_ref":""`, 1))
			assert.NilError(t, os.WriteFile(path, data, 0o600))
		}},
		{name: "changed diff", failed: true, edit: func(ws string) {
			assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileDiff), []byte(verifiedDiff+"\n"), 0o600))
		}},
		{name: "stale markers removed", wantCalls: 1, edit: func(ws string) {
			for _, name := range []string{artifact.FileFailed, artifact.FileSecurityBlock, FileStatus} {
				assert.NilError(t, os.WriteFile(filepath.Join(ws, name), []byte("old"), 0o600))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := verifiedWorkspace(t)
			if tt.edit != nil {
				tt.edit(ws)
			}
			resolve, requests := reviewServer(t, []string{jsonText(t, discovery{
				Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{},
			})})
			assert.NilError(t, Run(context.Background(), Options{Workspace: ws, VerifyFindings: true, Resolve: resolve}))
			assert.Equal(t, len(*requests), tt.wantCalls)
			assert.Equal(t, fileExists(filepath.Join(ws, artifact.FileFailed)), tt.failed)
			assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileSecurityBlock)))
			if tt.failed {
				assert.Assert(t, !readReview(t, ws).Verified, "invalid input must not claim verified provenance")
				assert.Assert(t, !fileExists(filepath.Join(ws, FileStatus)))
			}
		})
	}
}

func TestVerifiedSharedTokenBudgetFailsClosed(t *testing.T) {
	c := deletedCandidate()
	resolve, requests := reviewServer(t, []string{jsonText(t, discovery{
		Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{c},
	})})
	budget, err := model.NewBudgetWithTokenLimits(model.TokenLimits{MaxInputTokens: 20, MaxOutputTokens: 100})
	assert.NilError(t, err)
	ws := verifiedWorkspace(t)
	assert.NilError(t, Run(context.Background(), Options{
		Workspace: ws, VerifyFindings: true, Resolve: resolve, Budget: budget,
	}))
	assert.Equal(t, len(*requests), 1, "verification must not reset discovery's token usage")
	result := readReview(t, ws)
	assert.Equal(t, len(result.Comments)+len(result.SummaryFindings), 0)
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
}

func TestVerifiedNoExploration(t *testing.T) {
	ws := verifiedWorkspace(t)
	resolve, requests := reviewServer(t, []string{jsonText(t, discovery{
		Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{},
	})})
	assert.NilError(t, Run(context.Background(), Options{
		Workspace: ws, VerifyFindings: true, NoExploration: true, Resolve: resolve,
	}))
	assert.Equal(t, len(*requests), 1)
	_, hasTools := (*requests)[0]["tools"]
	assert.Assert(t, !hasTools)
	data, err := os.ReadFile(filepath.Join(ws, artifact.FileReview))
	assert.NilError(t, err)
	status, err := ReadStatus(&artifact.Workspace{Dir: ws}, data)
	assert.NilError(t, err)
	assert.Equal(t, status.State, "partial")
}

func TestVerifiedInventory(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		ws := verifiedWorkspace(t)
		resolve, requests := reviewServer(t, []string{jsonText(t, discovery{
			Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{},
		})})
		assert.NilError(t, Run(context.Background(), Options{
			Workspace: ws, VerifyFindings: true, NoInventory: disabled, Resolve: resolve,
		}))
		assert.Equal(t, len(*requests), 1)
		messages := jsonText(t, (*requests)[0]["messages"])
		assert.Equal(t, strings.Contains(messages, "BEGIN CHANGE INVENTORY"), !disabled)
		// The fixture source is not valid Go, so the inventory must say so.
		assert.Equal(t, strings.Contains(messages, "failed to parse"), !disabled)
	}
}

func TestVerifiedFileModeCoverage(t *testing.T) {
	const modeDiff = "diff --git a/script.sh b/script.sh\nold mode 100755\nnew mode 100644\n"
	tests := []struct {
		name        string
		withFinding bool
	}{
		{name: "mode only"},
		{name: "mode and source change", withFinding: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := verifiedWorkspace(t)
			patch := modeDiff
			candidates := []Candidate{}
			if tt.withFinding {
				patch = verifiedDiff + modeDiff
				candidates = append(candidates, deletedCandidate())
			}
			manifestPath := filepath.Join(ws, artifact.FileInputManifest)
			data, err := os.ReadFile(manifestPath)
			assert.NilError(t, err)
			var manifest artifact.InputManifest
			assert.NilError(t, json.Unmarshal(data, &manifest))
			manifest.DiffDigest = Digest([]byte(patch))
			assert.NilError(t, os.WriteFile(manifestPath, []byte(jsonText(t, manifest)), 0o600))
			assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileDiff), []byte(patch), 0o600))
			responses := []string{jsonText(t, discovery{
				Summary: "Removes executable permission.", ReviewScore: ReviewScore{2, "Small."}, Candidates: candidates,
			})}
			if tt.withFinding {
				c := candidates[0]
				responses = append(responses, jsonText(t, verdict{
					Summary:   "Removes executable permission and a guard.",
					Decisions: []decision{{ID: c.ID, Outcome: "accept", Reason: "Guard is removed.", Severity: "high", Evidence: c.Evidence}},
				}))
			}
			resolve, requests := reviewServer(t, responses)
			assert.NilError(t, Run(context.Background(), Options{Workspace: ws, VerifyFindings: true, Resolve: resolve}))
			assert.Equal(t, len(*requests), len(responses))
			for _, request := range *requests {
				assert.Assert(t, strings.Contains(jsonText(t, request["messages"]), "File modes before=\\\"100755\\\" head=\\\"100644\\\""))
			}
			data, err = os.ReadFile(filepath.Join(ws, artifact.FileReview))
			assert.NilError(t, err)
			status, err := ReadStatus(&artifact.Workspace{Dir: ws}, data)
			assert.NilError(t, err)
			assert.Equal(t, status.State, "partial")
			assert.Equal(t, status.Accepted, len(candidates))
			assert.Equal(t, len(status.Limitations), 1)
			assert.Assert(t, strings.Contains(status.Limitations[0], "File-mode change"))
			assert.Assert(t, strings.Contains(status.Limitations[0], "outside verified coverage"))
		})
	}
}

func TestFindingBodyOmitsUnsafePatches(t *testing.T) {
	tests := []struct {
		name   string
		remedy string
		want   string
	}{
		{name: "plain remedy", remedy: "Restore guard.", want: "Restore guard."},
		{name: "suggestion", remedy: "Restore guard.\n```suggestion\nunsafe replacement\n```", want: "Restore guard."},
		{name: "tilde fence", remedy: "~~~suggestion\nunsafe replacement\n~~~\nRestore guard.", want: "Restore guard."},
		{name: "unclosed fence", remedy: "Restore guard.\n```suggestion\nunsafe replacement", want: "Restore guard."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testCandidate()
			c.Remedy = tt.remedy
			assert.Equal(t, findingBody(c), "**Claim.** "+c.Claim+"\n\n**Trigger.** "+c.Trigger+"\n\n**Impact.** "+c.Impact+"\n\n**Fix.** "+tt.want)
		})
	}
}

func TestVerifiedKeepsBothFeedbackForms(t *testing.T) {
	ws := verifiedWorkspace(t)
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileExistingFeedback), []byte("Prior general review: input is validated by the caller."), 0o600))
	resolve, requests := reviewServer(t, []string{jsonText(t, discovery{
		Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{},
	})})
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, VerifyFindings: true, Resolve: resolve}))
	sent := jsonText(t, (*requests)[0]["messages"])
	assert.Assert(t, strings.Contains(sent, "Prior general review"))
	assert.Assert(t, strings.Contains(sent, "Structured inline feedback"))
}

func TestToolCoverageTracksMissingAndTruncatedSource(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		input      string
		incomplete bool
	}{
		{name: "complete", content: "source", input: `{"path":"a.go","start_line":1,"end_line":1}`},
		{name: "truncated", content: strings.Repeat("x", 16000), input: `{"path":"a.go","start_line":1,"end_line":1}`, incomplete: true},
		{name: "unavailable", content: "source", input: `{"path":"missing.go","start_line":1,"end_line":1}`, incomplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := coverageTools{Toolset: &source.Toolset{Head: &source.Snapshot{Files: map[string]string{"a.go": tt.content}}}}
			_, err := tools.Call(context.Background(), "read_file", json.RawMessage(tt.input))
			if !tt.incomplete {
				assert.NilError(t, err)
			}
			assert.Equal(t, tools.incomplete, tt.incomplete)
		})
	}
}

func TestNumberedSourcePreservesExactWhitespace(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		modified string
		differs  bool
	}{
		{
			name:     "Go spaces replaced with tabs",
			text:     "func parse(s string) (int, error) {\n n, _ := strconv.Atoi(s)\n return n, nil\n}",
			modified: "func parse(s string) (int, error) {\n\tn, _ := strconv.Atoi(s)\n\treturn n, nil\n}",
		},
		{
			name:     "Python indentation",
			text:     "if enabled:\n    run()\nfinish()",
			modified: "if enabled:\nrun()\nfinish()",
		},
		{
			name:     "whitespace next to a token",
			text:     "value := ` first\t  \n second `",
			modified: "value := `first\nsecond`",
			differs:  true,
		},
		{
			name:     "escapes and trailing whitespace",
			text:     "\tvalue := \"a\\\\b\\\"c\"  \r\n",
			modified: "\tvalue := \"a\\\\b\\\"c\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := strings.Split(tt.text, "\n")
			var hunk source.Hunk
			for i, line := range lines {
				hunk.Lines = append(hunk.Lines, source.DiffLine{Kind: "delete", OldLine: i + 1, Content: line})
			}
			for i, line := range lines {
				hunk.Lines = append(hunk.Lines, source.DiffLine{Kind: "add", NewLine: i + 1, Content: line})
			}
			parsed := &source.Diff{Files: []source.FileDiff{{
				OldPath: "file", NewPath: "file", Status: "modified", Hunks: []source.Hunk{hunk},
			}}}
			evidence, numbered := buildEvidenceContext(parsed, nil, nil)
			var decoded []string
			for _, line := range strings.Split(numbered, "\n") {
				if _, encoded, ok := strings.Cut(line, " source_json="); ok {
					var content string
					assert.NilError(t, json.Unmarshal([]byte(encoded), &content))
					decoded = append(decoded, content)
				}
			}
			assert.DeepEqual(t, decoded, append(append([]string{}, lines...), lines...))
			assert.Assert(t, !strings.Contains(numbered, "\t"), "tabs must be JSON-escaped")
			for _, revision := range []string{"head", "before"} {
				ref := Evidence{Revision: revision, Path: "file", Start: 1, End: len(lines), Quote: tt.text}
				assert.NilError(t, evidence.validate([]Evidence{ref}, "file", true))
				ref.Quote = tt.modified
				err := evidence.validate([]Evidence{ref}, "file", true)
				if tt.differs {
					assert.ErrorContains(t, err, "quote does not match")
				} else {
					assert.NilError(t, err, "indentation and blank runs are tolerated")
				}
				ref.Quote = tt.text + "x"
				assert.ErrorContains(t, evidence.validate([]Evidence{ref}, "file", true), "quote does not match")
			}
		})
	}
}

func TestSummaryModeCannotPublishFindings(t *testing.T) {
	ws := setupWorkspaceWithDiff(t, "some diff")
	resolve, requests := reviewServer(t, []string{`{"summary":"changes","comments":[{"path":"a.go","line":1,"severity":"high","body":"do not publish"}]}`})
	assert.NilError(t, Run(context.Background(), Options{
		Workspace: ws, VerifyFindings: true, TriggerComment: "/paco summary", Resolve: resolve,
	}))
	assert.Equal(t, len(*requests), 1)
	result := readReview(t, ws)
	assert.Equal(t, len(result.Comments), 0)
	assert.Assert(t, !result.Verified)
	assert.Assert(t, !strings.Contains(result.Summary, "do not publish"))
}

func TestResponseShape(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{name: "empty", text: "  ", want: "Discovery response shape: 2 bytes, first none, last none, 0 fences (0 before object), object start at -1, brackets before object false"},
		{name: "prose then object", text: "Here it is:\n{\"secret\":1}", want: "Discovery response shape: 24 bytes, first 'H', last '}', 0 fences (0 before object), object start at 12, brackets before object false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := responseShape("Discovery", tt.text)
			assert.Equal(t, got, tt.want)
			assert.Assert(t, !strings.Contains(got, "secret"))
		})
	}
}
