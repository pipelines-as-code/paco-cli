package command

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"
)

func TestExecRunnerEcho(t *testing.T) {
	r := &ExecRunner{}
	result, err := r.Run(context.Background(), "echo", []string{"hello"}, nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, string(result.Stdout), "hello\n")
}

func TestExecRunnerExitCode(t *testing.T) {
	r := &ExecRunner{}
	result, err := r.Run(context.Background(), "sh", []string{"-c", "exit 42"}, nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 42)
}

func TestExecRunnerStdin(t *testing.T) {
	r := &ExecRunner{}
	result, err := r.Run(context.Background(), "cat", nil, nil, []byte("input data"))
	assert.NilError(t, err)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, string(result.Stdout), "input data")
}
