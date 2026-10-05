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

	// Limits of the tool loop in Complete. The review system prompt states them.
	maxTurns           = 8
	maxToolCalls       = 24
	maxWebSearches     = 3
	maxToolResultBytes = 16000
)

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

// Result is the text of a completed response.
type Result struct {
	Text string
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
func (c *client) Complete(ctx context.Context, req Request) (Result, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt))},
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
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
	clientTools := params.Tools
	toolCalls, searches := 0, 0
	for range maxTurns {
		params.Tools = append([]anthropic.ToolUnionParam(nil), clientTools...)
		if req.WebSearch && searches < maxWebSearches {
			params.Tools = append(params.Tools, anthropic.ToolUnionParam{
				OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
					MaxUses: anthropic.Int(int64(maxWebSearches - searches)),
				},
			})
		}
		msg, err := c.streamMessage(ctx, params)
		if err != nil {
			return Result{}, err
		}
		var results []anthropic.ContentBlockParamUnion
		for _, block := range msg.Content {
			switch block.Type {
			case "server_tool_use":
				if !req.WebSearch || block.Name != "web_search" {
					return Result{}, &IncompleteError{Reason: "unexpected server tool"}
				}
				searches++
				if searches > maxWebSearches {
					return Result{}, &IncompleteError{Reason: "web search limit reached"}
				}
				fmt.Println("Model tool: web_search")
			case "web_search_tool_result":
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
				toolCalls++
				if toolCalls > maxToolCalls {
					return Result{}, &IncompleteError{Reason: "repository tool call limit reached"}
				}
				if !allowed[block.Name] {
					return Result{}, &IncompleteError{Reason: "unexpected repository tool"}
				}
				fmt.Printf("Model tool: %s\n", block.Name)
				output, callErr := req.Tools.Call(ctx, block.Name, block.Input)
				if ctx.Err() != nil {
					return Result{}, ctx.Err()
				}
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

func (c *client) streamMessage(ctx context.Context, params anthropic.MessageNewParams) (anthropic.Message, error) {
	stream := c.sdk.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	var msg anthropic.Message
	stopped := false
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return anthropic.Message{}, err
		}
		if ev.Type == "message_stop" {
			stopped = true
		}
	}
	if err := stream.Err(); err != nil {
		return anthropic.Message{}, err
	}
	if err := ctx.Err(); err != nil {
		return anthropic.Message{}, err
	}
	if !stopped {
		return anthropic.Message{}, &IncompleteError{Reason: "stream ended before message_stop"}
	}
	return msg, nil
}
