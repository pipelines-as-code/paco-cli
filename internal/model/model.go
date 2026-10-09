// Package model is paco's only path to Claude, through either Vertex AI or
// the Anthropic API.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/pipelines-as-code/paco-cli/internal/httpsafe"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	BackendVertex    = "vertex"
	BackendAnthropic = "anthropic"

	// Vertex takes the model id verbatim in the request path.
	DefaultVertexModel    = "claude-opus-4-6@default"
	DefaultAnthropicModel = "claude-opus-4-6"

	cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	tokenTimeout       = 30 * time.Second

	maxToolResultBytes = 16000
)

// DefaultLimits bounds the tool loop in Complete across one review. Time and
// token limits are the real guard rails; these keep a runaway loop finite.
var DefaultLimits = Limits{Turns: 24, ToolCalls: 80, WebSearches: 6}

// DiscoveryLimits caps the first pass of a verified review so verification
// keeps a share of DefaultLimits.
var DiscoveryLimits = Limits{Turns: 16, ToolCalls: 60, WebSearches: 4}

var validRegion = regexp.MustCompile(`^[a-z0-9-]+$`)

// Request is one review completion, optionally with tools.
type Request struct {
	System    string
	Prompt    string
	Model     string
	Effort    string
	Schema    map[string]any
	MaxTokens int64
	Tools     Toolset
	WebSearch bool
	// Progress receives public status lines; nil logs to stdout with pattern redaction.
	Progress *progress.Logger
	// InvestigationUpdates enables the bounded report_progress status tool.
	InvestigationUpdates bool
	// Budget nil creates a fresh default budget for this completion.
	Budget *Budget
	// Limits caps this completion without consuming unused allowances. For
	// two passes, use DiscoveryLimits for discovery and nil for verification.
	Limits *Limits
}

type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

type Toolset interface {
	Definitions() []Tool
	Call(context.Context, string, json.RawMessage) (string, error)
}

// Result contains text only on success, and usage even when Complete fails.
type Result struct {
	Text  string
	Usage Usage
}

// Client completes a request.
type Client interface {
	Complete(ctx context.Context, req Request) (Result, error)
}

// Resolved is a ready-to-use client plus what callers need to log and scrub.
type Resolved struct {
	Client       Client
	Backend      string
	DefaultModel string
	// Secrets are credential literals that must never appear in logs or
	// model output.
	Secrets []string
}

// Config holds test-only overrides; production code passes the zero value.
type Config struct {
	Transport http.RoundTripper
}

// Resolve picks the backend from the environment: ANTHROPIC_API_KEY first,
// then GOOGLE_APPLICATION_CREDENTIALS. SDK environment defaults (such as
// ANTHROPIC_BASE_URL) are never applied.
func Resolve(ctx context.Context, cfg Config) (*Resolved, error) {
	httpClient := &http.Client{Transport: cfg.Transport, CheckRedirect: httpsafe.CheckRedirect(false)}

	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		opts := []option.RequestOption{
			option.WithoutEnvironmentDefaults(),
			option.WithAPIKey(key),
			option.WithMaxRetries(0),
			option.WithHTTPClient(httpClient),
		}
		sdk := anthropic.NewClient(opts...)
		return &Resolved{
			Client:       &client{sdk: &sdk},
			Backend:      BackendAnthropic,
			DefaultModel: DefaultAnthropicModel,
			Secrets:      []string{key},
		}, nil
	}

	credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credFile == "" {
		return nil, errors.New("neither ANTHROPIC_API_KEY nor GOOGLE_APPLICATION_CREDENTIALS is set")
	}
	credData, err := os.ReadFile(credFile)
	if err != nil {
		return nil, fmt.Errorf("could not read the Vertex AI credentials file: %s (%w)", credFile, err)
	}
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal(credData, &sa); err != nil || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("the Vertex AI credentials file is missing required fields (client_email or private_key)")
	}
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = sa.ProjectID
	}
	if project == "" {
		return nil, errors.New("no Vertex AI project was configured or found in the service-account credentials")
	}
	region := os.Getenv("VERTEX_LOCATION")
	if region == "" {
		region = "global"
	}
	if !validRegion.MatchString(region) {
		return nil, fmt.Errorf("invalid VERTEX_LOCATION %q", region)
	}

	base := cfg.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	tokenClient := &http.Client{
		Timeout:       tokenTimeout,
		Transport:     &tokenTransport{ctx: ctx, base: base},
		CheckRedirect: httpsafe.CheckRedirect(false),
	}
	credentialCtx := context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	creds, err := google.CredentialsFromJSONWithType(credentialCtx, credData, google.ServiceAccount, cloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("could not load the Vertex AI credentials: %w", err)
	}
	vertexOpt, err := vertexCredentials(ctx, region, project, creds)
	if err != nil {
		return nil, err
	}
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithHTTPClient(httpClient),
		vertexOpt,
	}
	sdk := anthropic.NewClient(opts...)
	return &Resolved{
		Client:       &client{sdk: &sdk},
		Backend:      BackendVertex,
		DefaultModel: DefaultVertexModel,
		Secrets:      []string{sa.ClientEmail},
	}, nil
}

// The JWT token source uses PostForm without a request context. Tie its
// requests (including response reads) to the review lifetime as well.
type tokenTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	cleanup := func() {
		stop()
		cancel()
	}
	if t.ctx.Err() != nil {
		cleanup()
		return nil, t.ctx.Err()
	}
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		cleanup()
		return nil, err
	}
	resp.Body = &tokenBody{ReadCloser: resp.Body, cleanup: cleanup}
	return resp, nil
}

type tokenBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *tokenBody) Close() error {
	defer b.cleanup()
	return b.ReadCloser.Close()
}

// vertexCredentials turns the SDK's construction panic into an error.
func vertexCredentials(ctx context.Context, region, project string, creds *google.Credentials) (opt option.RequestOption, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("could not configure Vertex AI: %v", r)
		}
	}()
	return vertex.WithCredentials(ctx, region, project, creds), nil
}

// IncompleteError reports a response that ended without a usable answer.
type IncompleteError struct {
	Reason string
}

func (e *IncompleteError) Error() string { return "incomplete model response: " + e.Reason }

type client struct {
	sdk *anthropic.Client
}

// Complete runs a bounded tool loop and only returns a fully finished answer.
func (c *client) Complete(ctx context.Context, req Request) (result Result, err error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.Contains(req.Model, "/") {
		return Result{}, fmt.Errorf("invalid model id %q: use a bare Claude model id such as %q, provider prefixes are not supported", req.Model, DefaultVertexModel)
	}
	budget := req.Budget
	if budget == nil {
		budget = NewBudget()
	}
	limits, err := budget.begin(req.Limits)
	if err != nil {
		return Result{}, err
	}
	log := req.Progress
	if log == nil {
		log = progress.New(nil, nil)
	}
	var usage Usage
	defer func() {
		result.Usage = usage
		budget.end()
	}()
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt))},
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.InvestigationUpdates {
		params.System = append(params.System, anthropic.TextBlockParam{Text: InvestigationInstructions})
	}
	if req.Effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(req.Effort)
	}
	if req.Schema != nil {
		params.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: req.Schema}
	}

	allowed := map[string]bool{}
	if req.Tools != nil {
		for _, tool := range req.Tools.Definitions() {
			allowed[tool.Name] = true
			params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: tool.Properties, Required: tool.Required,
					ExtraFields: map[string]any{"additionalProperties": false},
				},
			}})
		}
	}
	if req.InvestigationUpdates {
		allowed["report_progress"] = true
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        "report_progress",
			Description: anthropic.String(ProgressToolDescription),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{"message": map[string]any{"type": "string", "maxLength": 240}}, Required: []string{"message"}, ExtraFields: map[string]any{"additionalProperties": false}},
		}})
	}
	clientTools := params.Tools
	hasTools := req.Tools != nil || req.WebSearch || req.InvestigationUpdates
	var toolCalls, searches int64
	toolsExhausted := false
	for turn := range limits.Turns {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		// The last turn, or the turn after the repository call allowance runs
		// out, has no tools so the model answers with what it has confirmed.
		final := hasTools && (turn == limits.Turns-1 || toolsExhausted)
		params.Tools = append([]anthropic.ToolUnionParam(nil), clientTools...)
		params.ToolChoice = anthropic.ToolChoiceUnionParam{}
		if final {
			if len(params.Tools) > 0 {
				params.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
			}
			if len(params.Messages) > 1 {
				params.Messages = appendFinalInstruction(params.Messages)
			}
		} else if req.WebSearch && searches < limits.WebSearches {
			params.Tools = append(params.Tools, anthropic.ToolUnionParam{
				OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
					MaxUses: anthropic.Int(limits.WebSearches - searches),
				},
			})
		}
		params.MaxTokens, err = budget.turn(req.MaxTokens)
		if err != nil {
			return Result{}, err
		}
		usage.ModelRequests++
		msg, err := c.streamMessage(ctx, params)
		reported := Usage{
			InputTokens:              msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens,
			OutputTokens:             msg.Usage.OutputTokens,
			CacheCreationInputTokens: msg.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     msg.Usage.CacheReadInputTokens,
		}
		usage.add(reported)
		var blockSearches int64
		for _, block := range msg.Content {
			if block.Type == "server_tool_use" && block.Name == "web_search" {
				blockSearches++
			}
		}
		// Usage is authoritative when higher, but partial streams may only
		// report tool blocks. Do not count the same search twice.
		messageSearches := max(blockSearches, msg.Usage.ServerToolUse.WebSearchRequests)
		searches += messageSearches
		budgetErr := budget.record(reported, messageSearches)
		if err != nil {
			return Result{}, err
		}
		if budgetErr != nil {
			return Result{}, budgetErr
		}
		if searches > limits.WebSearches {
			return Result{}, &IncompleteError{Reason: "web search limit reached"}
		}
		if !req.WebSearch && messageSearches > 0 {
			return Result{}, &IncompleteError{Reason: "unexpected server tool"}
		}
		var results []anthropic.ContentBlockParamUnion
		progressReported := false
		for _, block := range msg.Content {
			switch block.Type {
			case "server_tool_use":
				if !req.WebSearch || block.Name != "web_search" {
					return Result{}, &IncompleteError{Reason: "unexpected server tool"}
				}
				log.WebStart(block.Input)
			case "web_search_tool_result":
				log.WebEnd(block.RawJSON())
				var result struct {
					Content struct {
						Type      string `json:"type"`
						ErrorCode string `json:"error_code"`
					} `json:"content"`
				}
				// Successful search results have array content, not an error object.
				if json.Unmarshal([]byte(block.RawJSON()), &result) == nil &&
					result.Content.Type == "web_search_tool_result_error" {
					return Result{}, &IncompleteError{Reason: "web search failed: " + result.Content.ErrorCode}
				}
			case "tool_use":
				if msg.StopReason != anthropic.StopReasonToolUse {
					return Result{}, &IncompleteError{Reason: "tool request without tool_use stop reason"}
				}
				if final {
					if toolCalls >= limits.ToolCalls {
						return Result{}, &IncompleteError{Reason: "repository tool call limit reached"}
					}
					return Result{}, &IncompleteError{Reason: "model turn limit reached"}
				}
				if !allowed[block.Name] {
					return Result{}, &IncompleteError{Reason: "unexpected repository tool"}
				}
				if toolCalls >= limits.ToolCalls {
					toolsExhausted = true
					results = append(results, anthropic.NewToolResultBlock(block.ID, toolLimitMessage, true))
					continue
				}
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
				if err := budget.tool(); err != nil {
					return Result{}, err
				}
				toolCalls++
				if block.Name == "report_progress" {
					output, callErr := log.Report(block.Input, !progressReported)
					progressReported = true
					if callErr != nil {
						output = callErr.Error()
						log.Line("Investigation update rejected: %s", output)
					}
					results = append(results, anthropic.NewToolResultBlock(block.ID, output, callErr != nil))
					continue
				}
				log.ToolStart(block.Name, block.Input)
				started := time.Now()
				output, callErr := req.Tools.Call(ctx, block.Name, block.Input)
				if ctx.Err() != nil {
					log.ToolEnd(block.Name, "", ctx.Err(), time.Since(started))
					return Result{}, ctx.Err()
				}
				log.ToolEnd(block.Name, output, callErr, time.Since(started))
				if callErr != nil {
					output = callErr.Error()
				}
				if len(output) > maxToolResultBytes {
					return Result{}, &IncompleteError{Reason: fmt.Sprintf("repository tool result exceeded %d bytes", maxToolResultBytes)}
				}
				results = append(results, anthropic.NewToolResultBlock(block.ID, output, callErr != nil))
			}
		}
		switch msg.StopReason {
		case anthropic.StopReasonToolUse:
			if len(results) == 0 {
				return Result{}, &IncompleteError{Reason: "tool_use stop without tool requests"}
			}
			params.Messages = append(params.Messages, msg.ToParam(), anthropic.NewUserMessage(results...))
		case anthropic.StopReasonPauseTurn:
			if !req.WebSearch {
				return Result{}, &IncompleteError{Reason: "unexpected pause_turn"}
			}
			params.Messages = append(params.Messages, msg.ToParam())
		case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
			var text strings.Builder
			for _, block := range msg.Content {
				if block.Type == "text" {
					text.WriteString(block.Text)
				}
			}
			return Result{Text: text.String()}, nil
		case anthropic.StopReasonMaxTokens:
			return Result{}, &IncompleteError{Reason: "output token limit reached"}
		case anthropic.StopReasonRefusal:
			return Result{}, &IncompleteError{Reason: "the model refused to answer"}
		default:
			return Result{}, &IncompleteError{Reason: fmt.Sprintf("unexpected stop reason %q", msg.StopReason)}
		}
	}
	return Result{}, &IncompleteError{Reason: "model turn limit reached"}
}

const (
	toolLimitMessage = "Repository tool call limit reached. No more repository calls are available; answer now."
	finalInstruction = "Tool budget exhausted. Do not request more tools. Return the final answer now in the required format, " +
		"using only findings you confirmed. Omit hypotheses you could not check. " +
		"Start the reply with the JSON object itself, with no commentary before it."
)

// appendFinalInstruction adds the closing instruction to the pending tool
// results, or as its own user message after an assistant turn.
func appendFinalInstruction(messages []anthropic.MessageParam) []anthropic.MessageParam {
	text := anthropic.NewTextBlock(finalInstruction)
	last := &messages[len(messages)-1]
	if last.Role == anthropic.MessageParamRoleUser {
		last.Content = append(last.Content, text)
		return messages
	}
	return append(messages, anthropic.NewUserMessage(text))
}

func (c *client) streamMessage(ctx context.Context, params anthropic.MessageNewParams) (anthropic.Message, error) {
	stream := c.sdk.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	var msg anthropic.Message
	stopped := false
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return msg, err
		}
		if ev.Type == "message_stop" {
			stopped = true
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return msg, fmt.Errorf("model %q was not found; check the model id and that it is enabled for this project and region: %w", params.Model, err)
		}
		return msg, err
	}
	if err := ctx.Err(); err != nil {
		return msg, err
	}
	if !stopped {
		return msg, &IncompleteError{Reason: "stream ended before message_stop"}
	}
	return msg, nil
}

// InvestigationInstructions is included only when public model updates are enabled.
const InvestigationInstructions = "Use report_progress when beginning a meaningful check or changing investigation direction. Report only the concrete check underway, without conclusions, source excerpts, or internal reasoning. Keep updates brief and infrequent; do not emit one per file or tool call. Status updates and repository calls share the stated tool-call allowance. Final responses must still match the requested JSON schema."

// ProgressToolDescription identifies the model-facing status tool in evaluation digests.
const ProgressToolDescription = "Report a brief public status update describing the concrete check underway, such as checking that secret creation and lookup use the same normalization. These messages appear in terminal and CI logs. Do not include conclusions, findings, source excerpts, secrets, private identifiers, or internal reasoning. Use at most 240 characters without control characters. At most one update per model response, one every 30 seconds, and twelve per review are published. Calls consume the shared tool budget. Final responses must still match the requested JSON schema."
