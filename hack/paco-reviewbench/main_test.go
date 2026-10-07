package main

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestDiagnosticsConfig(t *testing.T) {
	for _, key := range []string{"RB_CONFIG_STRATEGY", "RB_CONFIG_WEB_SEARCH", "RB_CONFIG_EXPLORATION", "RB_CONFIG_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("RB_NWO", "owner/repo")
	t.Setenv("RB_PR_NUMBER", "1")
	t.Setenv("RB_BASE", "1111111111111111111111111111111111111111")
	t.Setenv("RB_HEAD", "2222222222222222222222222222222222222222")
	for _, path := range []string{"", "/work/out/diagnostics.json"} {
		t.Setenv("RB_DIAGNOSTICS", path)
		cfg, err := configFromEnv()
		assert.NilError(t, err)
		assert.Equal(t, cfg.Diagnostics, path)
	}
}
