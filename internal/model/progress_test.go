package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/progress"
	"gotest.tools/v3/assert"
)

func progressEvents(messages ...string) string {
	events := []string{evStart}
	for i, message := range messages {
		input, _ := json.Marshal(map[string]string{"message": message})
		events = append(events,
			fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"progress%d","name":"report_progress","input":{}}}`, i, i),
			fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, i, input),
			fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
	}
	return sse(append(events, evMessageDelta("tool_use"), evStop)...)
}

func TestProgressToolAcrossCompletions(t *testing.T) {
	var out bytes.Buffer
	log := progress.New(&out, []string{"secret-literal"})
	budget := NewBudget()
	n := 0
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		n++
		if n%2 == 1 {
			return response(200, "text/event-stream", progressEvents("Checking secret-literal normalization", "Checking consumers"))
		}
		return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
	}}
	c := anthropicClient(t, ft)
	for range 2 {
		result, err := c.Complete(context.Background(), Request{Prompt: "review", Model: "m", MaxTokens: 100, Budget: budget, Progress: log, InvestigationUpdates: true})
		assert.NilError(t, err)
		assert.Equal(t, result.Text, `{"summary":"ok"}`)
	}
	assert.Equal(t, budget.Snapshot().Used.ToolCalls, int64(4))
	assert.Equal(t, log.InvestigationCalls(), int64(4))
	assert.Equal(t, log.RepositoryCalls(), int64(0))
	assert.Equal(t, strings.Count(out.String(), "Investigation:"), 1)
	assert.Assert(t, strings.Contains(out.String(), "[REDACTED]") && !strings.Contains(out.String(), "secret-literal"))
	tools := ft.reqs[0].Body["tools"].([]any)
	assert.Equal(t, len(tools), 1)
	assert.Equal(t, tools[0].(map[string]any)["name"], "report_progress")
	results := ft.reqs[1].Body["messages"].([]any)[2].(map[string]any)["content"].([]any)
	assert.Equal(t, len(results), 2)
	assert.Assert(t, !strings.Contains(fmt.Sprint(results), "secret-literal"))
}

func TestProgressToolBudgetAndDisable(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		limit     int64
		wantError string
	}{
		{name: "shared tool limit", enabled: true, limit: 1},
		{name: "disabled status tool rejected", limit: 1, wantError: "unexpected repository tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, err := NewBudgetWithLimits(Limits{Turns: 3, ToolCalls: tt.limit})
			assert.NilError(t, err)
			var out bytes.Buffer
			log := progress.New(&out, nil)
			n := 0
			ft := &fakeTransport{respond: func(*http.Request) *http.Response {
				n++
				if n == 1 {
					return response(200, "text/event-stream", progressEvents("Checking callers", "Checking guards"))
				}
				return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
			}}
			_, err = anthropicClient(t, ft).Complete(context.Background(), Request{Prompt: "p", Model: "m", MaxTokens: 100, Budget: budget, Progress: log, InvestigationUpdates: tt.enabled})
			if tt.wantError != "" {
				assert.ErrorContains(t, err, tt.wantError)
				_, hasTools := ft.reqs[0].Body["tools"]
				assert.Assert(t, !hasTools)
				assert.Equal(t, log.InvestigationCalls(), int64(0))
			} else {
				assert.NilError(t, err)
				assert.Equal(t, budget.Snapshot().Used.ToolCalls, int64(1))
				assert.Equal(t, log.InvestigationCalls(), int64(1))
				assert.Equal(t, ft.reqs[1].Body["tool_choice"].(map[string]any)["type"], "none")
			}
		})
	}
}
