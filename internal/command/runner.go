package command

import (
	"bytes"
	"context"
	"os/exec"
)

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner interface {
	Run(ctx context.Context, executable string, args []string, env []string, stdin []byte) (Result, error)
}

type ExecRunner struct{}

func (r *ExecRunner) Run(ctx context.Context, executable string, args []string, env []string, stdin []byte) (Result, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
		err = nil
	}

	return Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: exitCode,
	}, err
}
