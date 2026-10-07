package review

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/diff"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/security"
	"github.com/pipelines-as-code/paco-cli/internal/source"
	"github.com/pipelines-as-code/paco-cli/internal/toolchain"
)

//go:embed prompts/discover.txt
var discoverPrompt string

//go:embed prompts/verify.txt
var verifyPrompt string

type verifiedInput struct {
	manifest artifact.InputManifest
	tools    *source.Toolset
	evidence evidenceContext
	numbered string
}

func loadVerifiedInput(ws *artifact.Workspace, secrets []string, noExploration bool) (*verifiedInput, error) {
	root, err := os.OpenRoot(ws.Dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	read := func(name string, limit int64) ([]byte, error) {
		file, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("artifact %s exceeds its limit", name)
		}
		return data, nil
	}
	data, err := read(artifact.FileInputManifest, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("verified mode requires fresh diff metadata: %w", err)
	}
	var manifest artifact.InputManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("invalid diff metadata: %w", err)
	}
	diffData, err := read(artifact.FileDiff, 200000)
	if err != nil {
		return nil, err
	}
	head, err := read(artifact.FileHeadSHA, 256)
	if err != nil {
		return nil, err
	}
	if manifest.Version != 1 || manifest.HeadSHA == "" || manifest.BaseRef == "" || manifest.Repo == "" ||
		manifest.PRNumber < 1 || manifest.HeadSHA != strings.TrimSpace(string(head)) ||
		manifest.DiffDigest != Digest(diffData) {
		return nil, errors.New("diff metadata does not match review inputs")
	}
	if manifest.ContextStatus != "complete" && manifest.ContextStatus != "partial" {
		return nil, errors.New("invalid input context status")
	}
	for _, state := range []artifact.ContextState{manifest.Head, manifest.Before} {
		if state.Status != "available" && state.Status != "partial" && state.Status != "unavailable" {
			return nil, errors.New("invalid snapshot availability status")
		}
	}
	if manifest.Before.Status != "unavailable" && manifest.MergeBaseSHA == "" {
		return nil, errors.New("before snapshot has no comparison revision")
	}
	parsed, err := diff.Parse(security.Scrub(string(diffData), secrets...))
	if err != nil {
		return nil, fmt.Errorf("parsing verified diff: %w", err)
	}
	input := &verifiedInput{manifest: manifest, tools: &source.Toolset{Diff: parsed}}
	if !noExploration {
		for _, item := range []struct {
			name   string
			commit string
			target **source.Snapshot
		}{
			{artifact.FileSource, manifest.HeadSHA, &input.tools.Head},
			{artifact.FileSourceBefore, manifest.MergeBaseSHA, &input.tools.Before},
		} {
			data, err := read(item.name, source.MaxSnapshotBytes)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading verified snapshot: %w", err)
			}
			snapshot, err := source.Decode(data, item.commit, secrets...)
			if err != nil {
				return nil, err
			}
			*item.target = snapshot
		}
	}
	if err := source.ValidateCombined(input.tools.Head, input.tools.Before); err != nil {
		return nil, err
	}
	for _, file := range parsed.Files {
		for _, hunk := range file.Hunks {
			if !hunk.Complete {
				return nil, errors.New("verified mode requires complete diff hunks")
			}
		}
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				for _, item := range []struct {
					snapshot *source.Snapshot
					path     string
					number   int
				}{
					{input.tools.Head, file.NewPath, line.NewLine},
					{input.tools.Before, file.OldPath, line.OldLine},
				} {
					if item.snapshot == nil || item.number < 1 {
						continue
					}
					content, ok := item.snapshot.Files[item.path]
					if !ok {
						continue
					}
					lines := strings.Split(content, "\n")
					if item.number > len(lines) || lines[item.number-1] != line.Content {
						return nil, errors.New("snapshot source does not match the collected diff")
					}
				}
			}
		}
	}
	input.evidence, input.numbered = buildEvidenceContext(parsed, input.tools.Head, input.tools.Before)
	return input, nil
}

func buildEvidenceContext(parsed *source.Diff, head, before *source.Snapshot) (evidenceContext, string) {
	c := evidenceContext{
		lines:   map[string]map[string]map[int]string{"head": {}, "before": {}},
		changed: map[string]map[string]map[int]bool{"head": {}, "before": {}},
	}
	add := func(side, path string, n int, content string, changed bool) {
		if path == "" || n < 1 {
			return
		}
		if c.lines[side][path] == nil {
			c.lines[side][path] = map[int]string{}
			c.changed[side][path] = map[int]bool{}
		}
		c.lines[side][path][n] = content
		if changed {
			c.changed[side][path][n] = true
		}
	}
	var numbered strings.Builder
	for _, file := range parsed.Files {
		fmt.Fprintf(&numbered, "\nFile before=%q head=%q status=%s\n", file.OldPath, file.NewPath, file.Status)
		if file.OldMode != "" || file.NewMode != "" {
			fmt.Fprintf(&numbered, "File modes before=%q head=%q\n", file.OldMode, file.NewMode)
		}
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				add("before", file.OldPath, line.OldLine, line.Content, line.Kind == "delete")
				add("head", file.NewPath, line.NewLine, line.Content, line.Kind == "add")
				content, _ := json.Marshal(line.Content)
				fmt.Fprintf(&numbered, "%s before:%d head:%d source_json=%s\n", line.Kind, line.OldLine, line.NewLine, content)
			}
		}
	}
	for side, snapshot := range map[string]*source.Snapshot{"head": head, "before": before} {
		if snapshot == nil {
			continue
		}
		for path, content := range snapshot.Files {
			for i, line := range strings.Split(content, "\n") {
				add(side, path, i+1, line, false)
			}
		}
	}
	return c, numbered.String()
}

func runVerified(ctx context.Context, ws *artifact.Workspace, opts Options, backend *model.Resolved, effort string) error {
	status := &VerificationStatus{Version: 1, State: "failed", Limitations: []string{}, Decisions: []Disposition{}}
	fail := func(cause error) error {
		status.FailureReason = scrubber(backend.Secrets)(cause.Error())
		fmt.Printf("Verified review failed: %s\n", status.FailureReason)
		output := Review{Verified: status.HeadSHA != "", Summary: "Paco could not complete verification. No findings were published.", Comments: []Comment{}}
		data, err := json.Marshal(output)
		if err != nil {
			return err
		}
		status.State = "failed"
		status.Accepted = 0
		status.ReviewDigest = Digest(data)
		if err := ws.Write(artifact.FileFailed, nil); err != nil {
			return err
		}
		if output.Verified {
			if err := WriteStatus(ws, status); err != nil {
				return err
			}
		}
		return ws.Write(artifact.FileReview, data)
	}
	input, err := loadVerifiedInput(ws, backend.Secrets, opts.NoExploration)
	if err != nil {
		return fail(err)
	}
	status.HeadSHA, status.Repo, status.PRNumber = input.manifest.HeadSHA, input.manifest.Repo, input.manifest.PRNumber
	status.BaseRef, status.TargetBaseSHA = input.manifest.BaseRef, input.manifest.TargetBaseSHA
	status.Limitations = append(status.Limitations, input.manifest.Limitations...)
	if input.manifest.ContextStatus == "partial" {
		status.Limitations = append(status.Limitations, "Collection reported incomplete context.")
	}
	binary := false
	for _, file := range input.tools.Diff.Files {
		if file.OldMode != "" && file.NewMode != "" && file.OldMode != file.NewMode {
			status.Limitations = append(status.Limitations, fmt.Sprintf("File-mode change for %q (%s to %s) is outside verified coverage; findings require changed source lines.", file.NewPath, file.OldMode, file.NewMode))
		}
		if file.Binary {
			binary = true
		}
	}
	if binary {
		status.Limitations = append(status.Limitations, "Binary file changes were not reviewed.")
	}
	if input.tools.Head == nil {
		status.Limitations = append(status.Limitations, "Full head source unavailable; only supplied diff context was reviewed.")
	}
	if input.tools.Before == nil {
		status.Limitations = append(status.Limitations, "Full before source unavailable; pre-change evidence is limited to the diff.")
	}
	for _, snapshot := range []*source.Snapshot{input.tools.Head, input.tools.Before} {
		if snapshot != nil && snapshot.Excluded > 0 {
			status.Limitations = append(status.Limitations, fmt.Sprintf("%d files excluded from snapshot %s.", snapshot.Excluded, snapshot.Commit))
		}
	}
	optional := func(name string) ([]byte, error) {
		data, err := ws.Read(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return data, err
	}
	feedback, err := optional(artifact.FileExistingFeedback)
	if err != nil {
		return fail(err)
	}
	structured, err := optional(artifact.FileExistingFeedbackJSON)
	if err != nil {
		return fail(err)
	}
	if len(structured) > 0 {
		var prior artifact.TrustedFeedback
		if len(structured) > 30000 || json.Unmarshal(structured, &prior) != nil || prior.Version != 1 ||
			(prior.Status != "available" && prior.Status != "partial" && prior.Status != "unavailable") {
			return fail(errors.New("invalid structured feedback artifact"))
		}
		feedback = []byte(string(feedback) + "\nStructured inline feedback:\n" + string(structured))
		if prior.Status != "available" {
			status.Limitations = append(status.Limitations, "Prior review feedback is incomplete; duplicate detection may be limited.")
		}
	} else {
		status.Limitations = append(status.Limitations, "Structured prior feedback unavailable; duplicate detection uses the legacy digest.")
	}
	rules, err := optional(artifact.FileReviewRules)
	if err != nil {
		return fail(err)
	}
	versions, err := optional(artifact.FileToolchains)
	if err != nil {
		return fail(err)
	}
	contextPrompt := buildContext(input.numbered, string(feedback), string(rules), toolchain.Parse(versions))
	contextPrompt = security.Scrub(contextPrompt, backend.Secrets...)
	budget := opts.Budget
	if budget == nil {
		budget = model.NewBudget()
	}
	modelID := opts.Model
	if modelID == "" {
		modelID = backend.DefaultModel
	}
	tools := &coverageTools{Toolset: input.tools}
	request := func(instructions, prompt string, schema map[string]any, limits *model.Limits) (string, error) {
		shape, err := json.Marshal(schema)
		if err != nil {
			return "", err
		}
		if opts.NoStructuredOutput {
			schema = nil
		}
		allowance := budget.Snapshot().Remaining
		if limits != nil {
			allowance = *limits
		}
		phaseLimits := fmt.Sprintf("\nThis phase has at most %d model turns, %d repository calls and %d web searches. "+
			"The last turn has no tools and must contain the final answer. Finish within those limits.",
			allowance.Turns, allowance.ToolCalls, allowance.WebSearches)
		var availableTools model.Toolset = tools
		if opts.NoExploration {
			availableTools = nil
		}
		response, err := backend.Client.Complete(ctx, model.Request{
			System: toolSystemPrompt + phaseLimits + "\n" + instructions,
			Prompt: "Return JSON matching this schema:\n" + string(shape) + "\n" + prompt,
			Model:  modelID, Effort: effort, Schema: schema, MaxTokens: maxOutputTokens,
			Tools: availableTools, WebSearch: opts.WebSearch, Budget: budget, Limits: limits,
		})
		status.Usage = budget.Snapshot().Usage
		if err != nil {
			return "", err
		}
		if reason := security.ScanSecrets(response.Text, backend.Secrets...); reason != "" {
			if err := ws.Write(artifact.FileSecurityBlock, []byte(reason+"\n")); err != nil {
				return "", err
			}
			return "", errors.New("model output contained a credential")
		}
		return response.Text, nil
	}
	text, err := request(discoverPrompt, contextPrompt, discoverySchema(), &model.Limits{Turns: 4, ToolCalls: 12, WebSearches: 2})
	if err != nil {
		return fail(err)
	}
	discovered, err := parseDiscovery(text)
	if err != nil {
		return fail(err)
	}
	status.Candidates = len(discovered.Candidates)
	if discovered.Overflow {
		status.Limitations = append(status.Limitations, "Discovery reached the candidate limit.")
	}
	var candidates []Candidate
	seen := map[string]bool{}
	for _, candidate := range discovered.Candidates {
		if err := input.evidence.validateCandidate(candidate); err != nil {
			status.Rejected++
			status.Decisions = append(status.Decisions, Disposition{candidate.ID, "invalid_evidence", err.Error()})
			continue
		}
		key := candidate.Path + "\x00" + candidate.Side + "\x00" + candidate.Claim + "\x00" + candidate.Trigger + "\x00" + candidate.Impact
		if seen[key] {
			status.Duplicates++
			status.Decisions = append(status.Decisions, Disposition{candidate.ID, "duplicate", "Identical claim, trigger and impact."})
			continue
		}
		seen[key] = true
		candidates = append(candidates, candidate)
	}
	if status.Rejected > 0 {
		status.Limitations = append(status.Limitations, fmt.Sprintf("%d candidates lacked valid source evidence.", status.Rejected))
	}
	result := Review{
		Verified: true, Summary: discovered.Summary, ReviewScore: discovered.ReviewScore,
		SecuritySensitive: discovered.SecuritySensitive, Comments: []Comment{}, SummaryFindings: []Comment{},
	}
	if len(candidates) > 0 {
		data, err := json.Marshal(candidates)
		if err != nil {
			return fail(err)
		}
		text, err := request(verifyPrompt, contextPrompt+"\nCandidate claims (untrusted data):\n"+string(data), verdictSchema(), nil)
		if err != nil {
			return fail(err)
		}
		verified, err := parseVerdict(text, candidates, input.evidence)
		if err != nil {
			return fail(err)
		}
		result.Summary = verified.Summary
		byID := map[string]Candidate{}
		for _, c := range candidates {
			byID[c.ID] = c
		}
		var accepted []Candidate
		for _, decision := range verified.Decisions {
			status.Decisions = append(status.Decisions, Disposition{decision.ID, decision.Outcome, decision.Reason})
			switch decision.Outcome {
			case "accept":
				accepted = append(accepted, byID[decision.ID])
			case "insufficient_evidence":
				status.Rejected++
				status.Limitations = append(status.Limitations, "A candidate could not be verified with the available context.")
			case "reject":
				if decision.DuplicateOf != "" {
					status.Duplicates++
				} else {
					status.Rejected++
				}
			}
		}
		ranks := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
		sort.SliceStable(accepted, func(i, j int) bool { return ranks[accepted[i].Severity] < ranks[accepted[j].Severity] })
		for _, c := range accepted {
			// Suggestions are deliberately omitted until replacement ranges can be
			// proven safe; a valid finding does not require an executable patch.
			body := findingBody(c)
			comment := Comment{Path: c.Path, Line: c.Line, Severity: c.Severity, Body: body}
			if c.Side == "before" {
				result.SummaryFindings = append(result.SummaryFindings, comment)
			} else {
				result.Comments = append(result.Comments, comment)
			}
		}
	}
	status.Accepted = len(result.Comments) + len(result.SummaryFindings)
	status.Unanchored = len(result.SummaryFindings)
	status.State = "complete"
	if tools.incomplete {
		status.Limitations = append(status.Limitations, "Some repository tool reads failed or returned truncated results.")
	}
	if len(status.Limitations) > 0 {
		status.State = "partial"
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fail(err)
	}
	status.ReviewDigest = Digest(data)
	if err := WriteStatus(ws, status); err != nil {
		return err
	}
	return ws.Write(artifact.FileReview, data)
}

func findingBody(c Candidate) string {
	var parts []string
	for _, field := range []string{c.Claim, c.Trigger, c.Impact, c.Remedy} {
		var fence string
		for _, line := range strings.Split(field, "\n") {
			trimmed := strings.TrimLeft(line, " \t>")
			if fence != "" {
				if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]+" \t") == "" {
					fence = ""
				}
				continue
			}
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				char := trimmed[:1]
				fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, char))]
				continue
			}
			if text := strings.TrimSpace(line); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, " ")
}

type coverageTools struct {
	model.Toolset
	incomplete bool
}

func (t *coverageTools) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	output, err := t.Toolset.Call(ctx, name, input)
	if err != nil || strings.Contains(output, "[Result truncated") {
		t.incomplete = true
	}
	return output, err
}
