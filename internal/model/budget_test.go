package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func budgetEvents(tools, searches int, reportedSearches int64, stop string) string {
	events := []string{evStart}
	for i := range tools {
		events = append(events,
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"call%d","name":"read_file","input":{}}}`, i, i),
			fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
	}
	for i := range searches {
		events = append(events,
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"search%d","name":"web_search","input":{}}}`, tools+i, i),
			fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, tools+i))
	}
	events = append(events,
		fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5,"server_tool_use":{"web_search_requests":%d}}}`, stop, reportedSearches),
		evStop)
	return sse(events...)
}

func TestCompleteSharedBudgetExactCaps(t *testing.T) {
	budget := NewBudget()
	tools := &testTools{}
	n := 0
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		n++
		searches := 0
		switch n {
		case 1:
			searches = 2
		case 5:
			searches = 1
		}
		stop, calls := "tool_use", 4
		if n%4 == 0 {
			stop, calls = "end_turn", 0
		}
		return response(200, "text/event-stream", budgetEvents(calls, searches, int64(searches), stop))
	}}
	c := anthropicClient(t, ft)
	req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, WebSearch: true, Budget: budget, Limits: &Limits{4, 12, 2}}
	for range 2 {
		result, err := c.Complete(context.Background(), req)
		assert.NilError(t, err)
		assert.DeepEqual(t, result.Usage, Usage{ModelRequests: 4, InputTokens: 4, OutputTokens: 20})
		req.Limits = nil
	}
	assert.Equal(t, tools.calls, 24)
	assert.Equal(t, len(ft.reqs), 8)
	assert.DeepEqual(t, budget.Snapshot(), BudgetSnapshot{
		Used: Limits{8, 24, 3}, Usage: Usage{ModelRequests: 8, InputTokens: 8, OutputTokens: 40},
	})
	for _, index := range []int{0, 4} {
		web := ft.reqs[index].Body["tools"].([]any)[1].(map[string]any)
		assert.Equal(t, web["max_uses"], float64(2-index/4))
	}
	_, err := c.Complete(context.Background(), req)
	assert.ErrorContains(t, err, "model turn limit")
	assert.Equal(t, len(ft.reqs), 8)
}

func TestCompleteDiscoveryReservation(t *testing.T) {
	tests := []struct {
		name      string
		tools     int
		want      string
		used      Limits
		remaining Limits
	}{
		{name: "turns", tools: 1, want: "model turn limit", used: Limits{4, 4, 0}, remaining: Limits{4, 20, 3}},
		{name: "repository calls", tools: 13, want: "repository tool call limit", used: Limits{1, 12, 0}, remaining: Limits{7, 12, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := NewBudget()
			tools := &testTools{}
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", budgetEvents(tt.tools, 0, 0, "tool_use"))
			}}
			c := anthropicClient(t, ft)
			_, err := c.Complete(context.Background(), Request{
				Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, Budget: budget, Limits: &Limits{4, 12, 2},
			})
			assert.ErrorContains(t, err, tt.want)
			assert.DeepEqual(t, budget.Snapshot().Used, tt.used)
			assert.DeepEqual(t, budget.Snapshot().Remaining, tt.remaining)
			assert.Equal(t, int64(tools.calls), tt.used.ToolCalls)
		})
	}
}

func TestCompleteUnusedDiscoveryAllowanceTransfers(t *testing.T) {
	budget := NewBudget()
	tools := &testTools{}
	n := 0
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		n++
		if n == 1 || n == 8 {
			return response(200, "text/event-stream", budgetEvents(0, 0, 0, "end_turn"))
		}
		searches := 0
		if n == 2 {
			searches = 3
		}
		return response(200, "text/event-stream", budgetEvents(4, searches, int64(searches), "tool_use"))
	}}
	c := anthropicClient(t, ft)
	req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, WebSearch: true, Budget: budget, Limits: &Limits{4, 12, 2}}
	_, err := c.Complete(context.Background(), req)
	assert.NilError(t, err)
	assert.DeepEqual(t, budget.Snapshot().Remaining, Limits{7, 24, 3})
	req.Limits = nil
	result, err := c.Complete(context.Background(), req)
	assert.NilError(t, err)
	assert.Equal(t, result.Usage.ModelRequests, int64(7))
	assert.DeepEqual(t, budget.Snapshot().Remaining, Limits{})
	assert.Equal(t, tools.calls, 24)
}

func TestCompleteProviderSearchAccounting(t *testing.T) {
	tests := []struct {
		name     string
		blocks   int
		reported int64
		limits   *Limits
		enabled  bool
		want     string
		used     int64
	}{
		{name: "no double count", blocks: 1, reported: 1, enabled: true, used: 1},
		{name: "usage only", reported: 2, enabled: true, used: 2},
		{name: "blocks exceed provider count", blocks: 4, reported: 1, enabled: true, want: "web search limit", used: 4},
		{name: "provider exceeds blocks", blocks: 1, reported: 4, enabled: true, want: "web search limit", used: 4},
		{name: "provider usage only excess", reported: 4, enabled: true, want: "web search limit", used: 4},
		{name: "discovery excess", blocks: 3, reported: 3, enabled: true, limits: &Limits{4, 12, 2}, want: "web search limit", used: 3},
		{name: "disabled searches", reported: 1, want: "unexpected server tool", used: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := NewBudget()
			tools := &testTools{}
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				// Put a repository request first: overrun detection must precede execution.
				return response(200, "text/event-stream", budgetEvents(1, tt.blocks, tt.reported, "tool_use"))
			}}
			req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, WebSearch: tt.enabled, Budget: budget, Limits: tt.limits}
			if req.Limits == nil {
				req.Limits = &Limits{1, 24, 3}
			}
			_, err := anthropicClient(t, ft).Complete(context.Background(), req)
			if tt.want == "" {
				assert.ErrorContains(t, err, "model turn limit")
				assert.Equal(t, tools.calls, 1)
			} else {
				assert.ErrorContains(t, err, tt.want)
				assert.Equal(t, tools.calls, 0)
			}
			assert.Equal(t, budget.Snapshot().Used.WebSearches, tt.used)
			assert.Equal(t, budget.Snapshot().Remaining.WebSearches, max(int64(0), 3-tt.used))
		})
	}
}

func TestCompleteCumulativeUsage(t *testing.T) {
	budget := NewBudget()
	n := 0
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		n++
		if n == 1 {
			return response(200, "text/event-stream", toolEvents("read_file"))
		}
		return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":3,"output_tokens":7}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":9,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}`, evStop))
	}}
	c := anthropicClient(t, ft)
	req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: &testTools{}, Budget: budget}
	first, err := c.Complete(context.Background(), req)
	assert.NilError(t, err)
	assert.DeepEqual(t, first.Usage, Usage{ModelRequests: 2, InputTokens: 11, OutputTokens: 14, CacheCreationInputTokens: 2, CacheReadInputTokens: 3})
	second, err := c.Complete(context.Background(), req)
	assert.NilError(t, err)
	assert.DeepEqual(t, second.Usage, Usage{ModelRequests: 1, InputTokens: 10, OutputTokens: 9, CacheCreationInputTokens: 2, CacheReadInputTokens: 3})
	assert.DeepEqual(t, budget.Snapshot().Usage, Usage{ModelRequests: 3, InputTokens: 21, OutputTokens: 23, CacheCreationInputTokens: 4, CacheReadInputTokens: 6})
}

func TestCompleteFailedUsagePersists(t *testing.T) {
	tests := []struct {
		name     string
		stream   string
		status   int
		want     string
		input    int64
		output   int64
		searches int64
	}{
		{name: "http failure", status: 400, stream: `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`, want: "bad request"},
		{name: "partial stream", status: 200, stream: sse(evStart, evMessageDelta("end_turn")), want: "message_stop", input: 1, output: 5},
		{name: "partial web stream", status: 200, stream: strings.TrimSuffix(webEvents(`[]`, "pause_turn"), sse(evStop)), want: "message_stop", input: 1, output: 5, searches: 1},
		{name: "provider failure", status: 200, stream: sse(evStart, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), want: "Overloaded", input: 1},
		{name: "invalid stream", status: 200, stream: sse(evStart, `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`), want: "expected index", input: 1},
		{name: "output truncation", status: 200, stream: sse(evStart, evMessageDelta("max_tokens"), evStop), want: "output token limit", input: 1, output: 5},
		{name: "web error", status: 200, stream: webEvents(`{"type":"web_search_tool_result_error","error_code":"unavailable"}`, "end_turn"), want: "web search failed", input: 1, output: 5, searches: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := NewBudget()
			n := 0
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				n++
				if n == 1 {
					return response(200, "text/event-stream", toolEvents("read_file"))
				}
				return response(tt.status, "text/event-stream", tt.stream)
			}}
			result, err := anthropicClient(t, ft).Complete(context.Background(), Request{
				Prompt: "p", Model: "m", MaxTokens: 100, Tools: &testTools{}, WebSearch: true, Budget: budget,
			})
			assert.ErrorContains(t, err, tt.want)
			assert.Equal(t, result.Text, "")
			assert.DeepEqual(t, result.Usage, Usage{ModelRequests: 2, InputTokens: 1 + tt.input, OutputTokens: 5 + tt.output})
			assert.DeepEqual(t, budget.Snapshot().Usage, result.Usage)
			assert.DeepEqual(t, budget.Snapshot().Used, Limits{2, 1, tt.searches})
		})
	}
}

func TestCompleteTokenBudgets(t *testing.T) {
	tests := []struct {
		name      string
		limits    TokenLimits
		wantFirst string
		wantNext  string
		maxTokens float64
	}{
		{name: "input exact", limits: TokenLimits{MaxInputTokens: 1}, wantNext: "input token budget", maxTokens: 100},
		{name: "output exact", limits: TokenLimits{MaxOutputTokens: 5}, wantNext: "output token budget", maxTokens: 5},
		{name: "provider output excess", limits: TokenLimits{MaxOutputTokens: 4}, wantFirst: "output token budget exceeded", wantNext: "output token budget", maxTokens: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, err := NewBudgetWithTokenLimits(tt.limits)
			assert.NilError(t, err)
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", budgetEvents(0, 0, 0, "end_turn"))
			}}
			c := anthropicClient(t, ft)
			req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Budget: budget}
			result, err := c.Complete(context.Background(), req)
			if tt.wantFirst == "" {
				assert.NilError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantFirst)
			}
			assert.Equal(t, result.Usage.OutputTokens, int64(5))
			_, err = c.Complete(context.Background(), req)
			assert.ErrorContains(t, err, tt.wantNext)
			assert.Equal(t, len(ft.reqs), 1)
			assert.Equal(t, ft.reqs[0].Body["max_tokens"], tt.maxTokens)
			assert.Equal(t, budget.Snapshot().Usage.ModelRequests, int64(1))
		})
	}
}

func TestCompleteTokenBudgetAcrossTurns(t *testing.T) {
	budget, err := NewBudgetWithTokenLimits(TokenLimits{MaxInputTokens: 3, MaxOutputTokens: 12})
	assert.NilError(t, err)
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", toolEvents("read_file"))
	}}
	_, err = anthropicClient(t, ft).Complete(context.Background(), Request{
		Prompt: "p", Model: "m", MaxTokens: 10, Tools: &testTools{}, Budget: budget,
	})
	assert.ErrorContains(t, err, "output token budget exceeded")
	for i, want := range []float64{10, 7, 2} {
		assert.Equal(t, ft.reqs[i].Body["max_tokens"], want)
	}
	assert.DeepEqual(t, budget.Snapshot().Usage, Usage{ModelRequests: 3, InputTokens: 3, OutputTokens: 15})
}

func TestCompleteInputBudgetIncludesCachedTokens(t *testing.T) {
	budget, err := NewBudgetWithTokenLimits(TokenLimits{MaxInputTokens: 5})
	assert.NilError(t, err)
	tools := &testTools{}
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		stream := strings.Replace(toolEvents("read_file"), `"input_tokens":1`, `"input_tokens":1,"cache_read_input_tokens":5`, 1)
		return response(200, "text/event-stream", stream)
	}}
	c := anthropicClient(t, ft)
	req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, Budget: budget}
	result, err := c.Complete(context.Background(), req)
	assert.ErrorContains(t, err, "input token budget exceeded")
	assert.Equal(t, result.Usage.InputTokens, int64(6))
	assert.Equal(t, tools.calls, 0)
	_, err = c.Complete(context.Background(), req)
	assert.ErrorContains(t, err, "input token budget exceeded")
	assert.Equal(t, len(ft.reqs), 1)
}

func TestCompleteBudgetCancellation(t *testing.T) {
	tests := []struct {
		name   string
		before bool
	}{
		{name: "before request", before: true},
		{name: "during request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := NewBudget()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			ft := &fakeTransport{respond: func(req *http.Request) *http.Response {
				got, ok := req.Context().Deadline()
				assert.Assert(t, ok)
				assert.Equal(t, got, deadline)
				cancel()
				return response(200, "text/event-stream", toolEvents("read_file"))
			}}
			c := anthropicClient(t, ft)
			tools := &testTools{}
			if tt.before {
				cancel()
			}
			result, err := c.Complete(ctx, Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, Budget: budget})
			assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
			assert.Equal(t, tools.calls, 0)
			assert.Equal(t, result.Usage.ModelRequests, int64(len(ft.reqs)))
			assert.DeepEqual(t, budget.Snapshot().Usage, result.Usage)
			if tt.before {
				assert.Equal(t, len(ft.reqs), 0)
			} else {
				assert.Equal(t, len(ft.reqs), 1)
			}
			_, err = budget.begin(nil)
			assert.NilError(t, err, "cancellation must release the budget")
			budget.end()
		})
	}
}

func TestBudgetInvalidLimits(t *testing.T) {
	for _, limits := range []TokenLimits{{MaxInputTokens: -1}, {MaxOutputTokens: -1}} {
		_, err := NewBudgetWithTokenLimits(limits)
		assert.ErrorContains(t, err, "must not be negative")
	}
	for _, limits := range []Limits{{Turns: -1}, {ToolCalls: -1}, {WebSearches: -1}} {
		_, err := NewBudget().begin(&limits)
		assert.ErrorContains(t, err, "must not be negative")
	}
}

type cancellingTools struct {
	testTools
	cancel context.CancelFunc
}

func (t *cancellingTools) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t.cancel()
	return t.testTools.Call(ctx, name, input)
}

func TestCompleteCancellationAfterUsage(t *testing.T) {
	budget := NewBudget()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := &cancellingTools{cancel: cancel}
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", budgetEvents(2, 1, 1, "tool_use"))
	}}
	result, err := anthropicClient(t, ft).Complete(ctx, Request{
		Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, WebSearch: true, Budget: budget,
	})
	assert.Assert(t, errors.Is(err, context.Canceled))
	assert.DeepEqual(t, result.Usage, Usage{ModelRequests: 1, InputTokens: 1, OutputTokens: 5})
	assert.DeepEqual(t, budget.Snapshot().Usage, result.Usage)
	assert.DeepEqual(t, budget.Snapshot().Used, Limits{1, 1, 1})
	assert.Equal(t, tools.calls, 1)
	assert.Equal(t, len(ft.reqs), 1)
}

func TestCompleteFailedRepositoryCallsConsumeBudget(t *testing.T) {
	budget := NewBudget()
	tools := &testTools{err: errors.New("missing file")}
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", budgetEvents(13, 0, 0, "tool_use"))
	}}
	c := anthropicClient(t, ft)
	req := Request{Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, Budget: budget, Limits: &Limits{4, 12, 2}}
	for range 2 {
		_, err := c.Complete(context.Background(), req)
		assert.ErrorContains(t, err, "repository tool call limit")
		req.Limits = nil
	}
	assert.Equal(t, tools.calls, 24)
	assert.DeepEqual(t, budget.Snapshot().Used, Limits{2, 24, 0})
	assert.DeepEqual(t, budget.Snapshot().Remaining, Limits{6, 0, 3})
}

func TestCompleteExplicitZeroLimits(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		want   string
		turns  int
	}{
		{name: "no turns", limits: Limits{}, want: "model turn limit"},
		{name: "no repository calls", limits: Limits{Turns: 1}, want: "repository tool call limit", turns: 1},
		{name: "no web searches", limits: Limits{Turns: 1, ToolCalls: 1}, want: "web search limit", turns: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := NewBudget()
			tools := &testTools{}
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				if tt.name == "no web searches" {
					return response(200, "text/event-stream", budgetEvents(1, 1, 1, "tool_use"))
				}
				return response(200, "text/event-stream", toolEvents("read_file"))
			}}
			_, err := anthropicClient(t, ft).Complete(context.Background(), Request{
				Prompt: "p", Model: "m", MaxTokens: 100, Tools: tools, WebSearch: true, Budget: budget, Limits: &tt.limits,
			})
			assert.ErrorContains(t, err, tt.want)
			assert.Equal(t, tools.calls, 0)
			assert.Equal(t, len(ft.reqs), tt.turns)
			for _, req := range ft.reqs {
				assert.Equal(t, len(req.Body["tools"].([]any)), 1, "zero web allowance must omit the server tool")
			}
		})
	}
}

func TestCompleteSharedBudgetRejectsOverlap(t *testing.T) {
	budget := NewBudget()
	var c Client
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		assert.DeepEqual(t, budget.Snapshot().Used, Limits{Turns: 1})
		_, err := c.Complete(context.Background(), Request{Budget: budget})
		assert.ErrorContains(t, err, "already in use")
		return response(200, "text/event-stream", budgetEvents(0, 0, 0, "end_turn"))
	}}
	c = anthropicClient(t, ft)
	_, err := c.Complete(context.Background(), Request{Prompt: "p", Model: "m", MaxTokens: 100, Budget: budget})
	assert.NilError(t, err)
	assert.Equal(t, len(ft.reqs), 1)
	assert.Equal(t, budget.Snapshot().Usage.ModelRequests, int64(1))
}
