package reviewbench_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/reviewbench"
	"gotest.tools/v3/assert"
)

type diagnosticTransport struct{ calls int }

func (tr *diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.calls++
	var stream strings.Builder
	event := func(kind, data string) { fmt.Fprintf(&stream, "event: %s\ndata: %s\n\n", kind, data) }
	event("message_start", `{"type":"message_start","message":{"id":"msg","type":"message","role":"assistant","model":"test","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`)
	reason := "end_turn"
	if tr.calls == 1 {
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"read","name":"read_file","input":{}}}`)
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"div.go\",\"start_line\":1,\"end_line\":4}"}}`)
		reason = "tool_use"
	} else {
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		text, _ := json.Marshal(`{"summary":"Checked the change.","comments":[]}`)
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(text)+`}}`)
	}
	event("content_block_stop", `{"type":"content_block_stop","index":0}`)
	event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`"},"usage":{"output_tokens":5}}`)
	event("message_stop", `{"type":"message_stop"}`)
	return &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(stream.String())), Request: req,
	}, nil
}

func TestDiagnosticsActualUsage(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	transport := &diagnosticTransport{}
	cfg := config(t, divDiff)
	cfg.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
	cfg.Review.ReasoningEffort = "HIGH"
	cfg.Review.Resolve = func(ctx context.Context) (*model.Resolved, error) {
		return model.Resolve(ctx, model.Config{Transport: transport})
	}
	assert.NilError(t, reviewbench.Run(context.Background(), cfg))
	report := readDiagnostics(t, cfg.Diagnostics)
	assert.Equal(t, report.Summary, "Checked the change.")
	assert.Equal(t, report.Requests[0].Effort, "high")
	assert.Equal(t, report.Requests[0].Model, model.DefaultAnthropicModel)
	assert.Equal(t, report.Budget.Usage.ModelRequests, int64(2))
	assert.Equal(t, report.Budget.Usage.InputTokens, int64(20))
	assert.Equal(t, report.Budget.Usage.OutputTokens, int64(10))
	assert.Equal(t, report.Budget.Used.ToolCalls, int64(1))
	assert.Equal(t, report.Budget.Used.WebSearches, int64(0))
}
