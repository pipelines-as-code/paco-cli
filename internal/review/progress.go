package review

import (
	"sync"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
)

type reviewProgress struct {
	log                            *progress.Logger
	budget                         *model.Budget
	started                        time.Time
	mu                             sync.Mutex
	phase                          string
	state                          string
	accepted, rejected, duplicates int
	limitations                    []string
}

func (p *reviewProgress) phaseStarted(phase string) {
	p.mu.Lock()
	p.phase = phase
	p.mu.Unlock()
	p.log.Line("%s started", phase)
}

func (p *reviewProgress) logUsage() {
	b := p.budget.Snapshot()
	p.log.Line("Elapsed: %s; repository calls: %d; tool calls: %d/%d; model turns: %d/%d; web searches: %d/%d",
		time.Since(p.started).Round(time.Second).String(), p.log.RepositoryCalls(), b.Used.ToolCalls, b.Used.ToolCalls+b.Remaining.ToolCalls,
		b.Used.Turns, b.Used.Turns+b.Remaining.Turns, b.Used.WebSearches, b.Used.WebSearches+b.Remaining.WebSearches)
	p.log.Line("Reported tokens: input %d, output %d; cache creation %d, cache read %d (included in input)", b.Usage.InputTokens, b.Usage.OutputTokens, b.Usage.CacheCreationInputTokens, b.Usage.CacheReadInputTokens)
}

func (p *reviewProgress) heartbeat(interval time.Duration) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				p.mu.Lock()
				phase := p.phase
				p.mu.Unlock()
				p.log.Line("%s still running", phase)
				p.logUsage()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop); <-done }) }
}

func (p *reviewProgress) finish(err error) {
	if err != nil || p.state == "failed" {
		p.log.Line("Review failed: no findings ready")
		if err != nil {
			p.log.Line("Failure: %s", err.Error())
		}
	} else {
		p.log.Line("Review completed: %d findings ready; coverage %s", p.accepted, p.state)
	}
	p.log.Line("Candidates: %d accepted, %d rejected, %d duplicates", p.accepted, p.rejected, p.duplicates)
	for _, limitation := range p.limitations {
		p.log.Line("Coverage limitation: %s", limitation)
	}
	p.logUsage()
}

func (p *reviewProgress) decision(c Candidate, d Disposition, severity string) {
	label := map[string]string{"accept": "Accepted", "reject": "Rejected", "insufficient_evidence": "Insufficient evidence", "invalid_evidence": "Rejected", "duplicate": "Duplicate"}[d.Outcome]
	if severity != "" {
		label += " [" + severity + "]"
	}
	p.log.Line("%s: %s - %s:%d [%s] (candidate %s): %s", label, c.Claim, c.Path, c.Line, c.Side, c.ID, d.Reason)
}
