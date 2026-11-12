package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func testToolset() *Toolset {
	return &Toolset{Skills: []Skill{
		{Name: "alpha", Description: "first skill", Body: "Look at the first thing."},
		{Name: "beta", Description: "second skill", Body: "Look at the second thing."},
	}}
}

func TestToolsetDefinitions(t *testing.T) {
	defs := testToolset().Definitions()
	assert.Equal(t, len(defs), 1)
	assert.Equal(t, defs[0].Name, ToolName)
	assert.DeepEqual(t, defs[0].Required, []string{"name"})
	enum, ok := defs[0].Properties["name"].(map[string]any)["enum"].([]string)
	assert.Assert(t, ok)
	assert.DeepEqual(t, enum, []string{"alpha", "beta"})
	assert.Assert(t, strings.Contains(defs[0].Description, "target branch"))

	assert.Equal(t, len((&Toolset{}).Definitions()), 0)
}

func TestToolsetCall(t *testing.T) {
	ctx := context.Background()
	out, err := testToolset().Call(ctx, ToolName, json.RawMessage(`{"name":"beta"}`))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "second skill"))
	assert.Assert(t, strings.Contains(out, "Look at the second thing."))

	tests := []struct {
		name    string
		tool    string
		input   string
		wantErr string
	}{
		{name: "unknown tool", tool: "read_file", input: `{"name":"beta"}`, wantErr: "unknown tool"},
		{name: "unknown skill", tool: ToolName, input: `{"name":"gamma"}`, wantErr: `unknown skill "gamma"; available skills: alpha, beta`},
		{name: "extra field", tool: ToolName, input: `{"name":"beta","path":"a"}`, wantErr: "invalid tool arguments"},
		{name: "trailing object", tool: ToolName, input: `{"name":"beta"}{}`, wantErr: "one JSON object"},
		{name: "oversized input", tool: ToolName, input: `{"name":"` + strings.Repeat("a", 5000) + `"}`, wantErr: "exceeds 4096 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testToolset().Call(ctx, tt.tool, json.RawMessage(tt.input))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestToolsetResultFitsModelLimit(t *testing.T) {
	// The model fails a review when one tool result exceeds 16000 bytes.
	const maxToolResultBytes = 16000
	tools := &Toolset{Skills: []Skill{{
		Name:        "big",
		Description: strings.Repeat("d", MaxDescriptionBytes),
		Body:        strings.Repeat("b", MaxFileBytes),
	}}}
	out, err := tools.Call(context.Background(), ToolName, json.RawMessage(`{"name":"big"}`))
	assert.NilError(t, err)
	assert.Assert(t, len(out) < maxToolResultBytes, "result is %d bytes", len(out))
}
