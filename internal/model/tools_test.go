package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

type testTools struct {
	calls int
	err   error
}

func (*testTools) Definitions() []Tool {
	return []Tool{{Name: "read_file", Description: "Read snapshot file", Properties: map[string]any{
		"path": map[string]any{"type": "string"},
	}, Required: []string{"path"}}}
}

func (t *testTools) Call(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	t.calls++
	return "1: package test", t.err
}

func toolEvents(name string) string {
	return sse(evStart,
		fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call1","name":%q,"input":{}}}`, name),
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}`,
		evBlockStop, evMessageDelta("tool_use"), evStop)
}

func TestCompleteRepositoryToolLoop(t *testing.T) {
	tests := []struct {
		name    string
		callErr error
	}{
		{name: "successful read"},
		{name: "invalid input returned to model", callErr: errors.New("file not found")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &testTools{err: tt.callErr}
			n := 0
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				n++
				if n == 1 {
					return response(200, "text/event-stream", toolEvents("read_file"))
				}
				return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
			}}
			c := anthropicClient(t, ft)
			result, err := c.Complete(context.Background(), Request{Prompt: "review", Model: "m", MaxTokens: 100, Tools: tools})
			assert.NilError(t, err)
			assert.Equal(t, tools.calls, 1)
			assert.Equal(t, result.Text, `{"summary":"ok"}`)
			assert.Equal(t, len(ft.reqs), 2)
			first := ft.reqs[0].Body["tools"].([]any)[0].(map[string]any)
			assert.Equal(t, first["name"], "read_file")
			messages := ft.reqs[1].Body["messages"].([]any)
			assert.Equal(t, len(messages), 3)
			resultBlock := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
			assert.Equal(t, resultBlock["type"], "tool_result")
			assert.Equal(t, resultBlock["tool_use_id"], "call1")
			assert.Equal(t, resultBlock["is_error"], tt.callErr != nil)
			assert.Equal(t, messages[1].(map[string]any)["role"], "assistant")
		})
	}
}

func TestCompleteToolFailures(t *testing.T) {
	tests := []struct {
		name, stream, want string
		calls              int
	}{
		{name: "unknown tool", stream: toolEvents("bash"), want: "unexpected repository tool"},
		{name: "incomplete tool stream", stream: strings.TrimSuffix(toolEvents("read_file"), "event: message_stop\ndata: "+evStop+"\n\n"), want: "message_stop"},
		{name: "turn limit", stream: toolEvents("read_file"), want: "turn limit", calls: 7},
		{name: "empty tool turn", stream: sse(evStart, evMessageDelta("tool_use"), evStop), want: "without tool requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", tt.stream)
			}}
			tools := &testTools{}
			_, err := anthropicClient(t, ft).Complete(context.Background(), Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools})
			assert.ErrorContains(t, err, tt.want)
			assert.Equal(t, tools.calls, tt.calls)
		})
	}
}

func TestCompleteFinalTurnAnswers(t *testing.T) {
	tests := []struct {
		name   string
		limits *Limits
		tools  int
		calls  int
		reqs   int
	}{
		{name: "turn limit", tools: 1, calls: 7, reqs: 8},
		{name: "repository call limit", limits: &Limits{Turns: 8, ToolCalls: 3, WebSearches: 0}, tools: 2, calls: 3, reqs: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &testTools{}
			ft := &fakeTransport{}
			ft.respond = func(*http.Request) *http.Response {
				ft.mu.Lock()
				_, final := ft.reqs[len(ft.reqs)-1].Body["tool_choice"]
				ft.mu.Unlock()
				if final {
					return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
				}
				return response(200, "text/event-stream", budgetEvents(tt.tools, 0, 0, "tool_use"))
			}
			result, err := anthropicClient(t, ft).Complete(context.Background(), Request{
				Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, WebSearch: true, Limits: tt.limits,
			})
			assert.NilError(t, err)
			assert.Equal(t, result.Text, `{"summary":"ok"}`)
			assert.Equal(t, tools.calls, tt.calls)
			assert.Equal(t, len(ft.reqs), tt.reqs)
			for _, req := range ft.reqs[:len(ft.reqs)-1] {
				_, hasChoice := req.Body["tool_choice"]
				assert.Assert(t, !hasChoice)
			}
			final := ft.reqs[len(ft.reqs)-1].Body
			assert.DeepEqual(t, final["tool_choice"], map[string]any{"type": "none"})
			finalTools := final["tools"].([]any)
			assert.Equal(t, len(finalTools), 1, "the final turn drops web search")
			assert.Equal(t, finalTools[0].(map[string]any)["name"], "read_file")
			messages := final["messages"].([]any)
			content := messages[len(messages)-1].(map[string]any)["content"].([]any)
			last := content[len(content)-1].(map[string]any)
			assert.Equal(t, last["type"], "text")
			assert.Equal(t, last["text"], finalInstruction)
			if tt.limits != nil {
				limited := content[len(content)-2].(map[string]any)
				assert.Equal(t, limited["is_error"], true)
			}
		})
	}
}

func TestCompleteFinalTurnToolRequestFails(t *testing.T) {
	tests := []struct {
		name   string
		limits *Limits
		want   string
	}{
		{name: "turn limit", want: "model turn limit"},
		{name: "repository call limit", limits: &Limits{Turns: 8, ToolCalls: 1, WebSearches: 0}, want: "repository tool call limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", budgetEvents(2, 0, 0, "tool_use"))
			}}
			_, err := anthropicClient(t, ft).Complete(context.Background(), Request{
				Prompt: "p", Model: "m", MaxTokens: 100, Tools: &testTools{}, Limits: tt.limits,
			})
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func webEvents(content string, stop string) string {
	return sse(evStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Go slices documentation\"}"}}`,
		evBlockStop,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv1","content":`+content+`}}`,
		`{"type":"content_block_stop","index":1}`,
		evMessageDelta(stop), evStop)
}

func TestCompleteWebSearch(t *testing.T) {
	n := 0
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		n++
		if n == 1 {
			return response(200, "text/event-stream", webEvents(`[]`, "pause_turn"))
		}
		return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
	}}
	result, err := anthropicClient(t, ft).Complete(context.Background(), Request{Prompt: "p", Model: "m", MaxTokens: 100, WebSearch: true})
	assert.NilError(t, err)
	assert.Equal(t, result.Text, `{"summary":"ok"}`)
	assert.Equal(t, len(ft.reqs), 2)
	tool := ft.reqs[0].Body["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, tool["type"], "web_search_20250305")
	assert.Equal(t, tool["max_uses"], float64(3))
	_, hasCallers := tool["allowed_callers"]
	assert.Assert(t, !hasCallers, "Vertex basic web search rejects allowed_callers")
	tool = ft.reqs[1].Body["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, tool["max_uses"], float64(2))
	assert.Equal(t, len(ft.reqs[1].Body["messages"].([]any)), 2)
}

func TestCompleteWebSearchFailure(t *testing.T) {
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", webEvents(`{"type":"web_search_tool_result_error","error_code":"unavailable"}`, "end_turn"))
	}}
	_, err := anthropicClient(t, ft).Complete(context.Background(), Request{Prompt: "p", Model: "m", MaxTokens: 100, WebSearch: true})
	assert.ErrorContains(t, err, "web search failed: unavailable")
}
