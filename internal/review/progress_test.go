package review

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
	"gotest.tools/v3/assert"
)

type heartbeatWriter struct {
	mu sync.Mutex
	bytes.Buffer
	tick chan struct{}
}

func (w *heartbeatWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.Contains(data, []byte("still running")) {
		select {
		case w.tick <- struct{}{}:
		default:
		}
	}
	return w.Buffer.Write(data)
}

func TestHeartbeatStopsBeforeCompletion(t *testing.T) {
	out := &heartbeatWriter{tick: make(chan struct{}, 1)}
	p := &reviewProgress{log: progress.New(out, nil), budget: model.NewBudget(), started: time.Now(), state: "complete"}
	p.phaseStarted("Verification")
	stop := p.heartbeat(time.Millisecond)
	select {
	case <-out.tick:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not run")
	}
	stop()
	stop()
	p.finish(nil)
	before := out.String()
	time.Sleep(5 * time.Millisecond)
	assert.Equal(t, out.String(), before)
	assert.Assert(t, strings.Contains(before, "Verification still running"))
	assert.Assert(t, strings.Contains(before, "tool calls: 0/80; model turns: 0/24; web searches: 0/6"))
	assert.Assert(t, strings.LastIndex(before, "still running") < strings.Index(before, "Review completed:"))
}

func TestSingleProgressDoesNotDumpResponses(t *testing.T) {
	var out bytes.Buffer
	ws := setupWorkspaceWithDiff(t, "diff")
	fake := &fakeClient{text: "unparseable private source"}
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: fakeResolve(fake), LogWriter: &out}))
	assert.Assert(t, strings.Contains(out.String(), "Review started"))
	assert.Assert(t, strings.Contains(out.String(), "Review failed: no findings ready"))
	assert.Assert(t, !strings.Contains(out.String(), fake.text))
	assert.Assert(t, fake.got.Budget != nil && fake.got.Progress != nil)
}
