package main

import (
	"flag"
	"os"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/eval"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"gotest.tools/v3/assert"
)

func TestRequiresExplicitLivePermission(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no authorization"},
		{name: "no cases or token bounds", args: []string{"--live"}},
		{name: "no output bound", args: []string{"--live", "--cases", "division-guard-removed", "--output", "unused.json", "--max-input-tokens", "10"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalFlags, originalArgs := flag.CommandLine, os.Args
			t.Cleanup(func() { flag.CommandLine, os.Args = originalFlags, originalArgs })
			flag.CommandLine = flag.NewFlagSet("paco-eval", flag.ContinueOnError)
			os.Args = append([]string{"paco-eval", "--fixtures", "../../internal/review/testdata/eval/cases.json"}, tt.args...)
			assert.ErrorContains(t, run(), "live runs require")
			assert.Equal(t, flag.Lookup("no-structured-output").Value.String(), "true")
			assert.Equal(t, flag.Lookup("inventory").Value.String(), "true")
			assert.Equal(t, flag.Lookup("investigation-updates").Value.String(), "true")
		})
	}
}

func TestRunCaseRecordsCredentialFailure(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	cases, err := eval.Load("../../internal/review/testdata/eval/cases.json")
	assert.NilError(t, err)
	result, err := runCase(cases[0], 0, "verified", "", "low", true, false, false, model.NewBudget())
	assert.NilError(t, err)
	assert.Assert(t, result.Failed)
	assert.Assert(t, !result.StructuredOutput)
	assert.Assert(t, !result.Inventory)
	assert.Assert(t, !result.InvestigationUpdates)
	assert.Equal(t, result.Usage.ModelRequests, int64(0))
	assert.Equal(t, len(result.PromptDigest), 64)
	assert.Equal(t, len(result.InputDigest), 64)
}
