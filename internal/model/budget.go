package model

import (
	"errors"
	"sync"
)

// Limits bounds one completion. With Request.Limits nil, the completion can use
// the entire remaining budget. Explicit zero fields prohibit that operation.
type Limits struct {
	Turns       int64
	ToolCalls   int64
	WebSearches int64
}

// Usage counts attempted model requests and provider-reported tokens, including
// partial and failed responses. Tokens not reported by the provider are unknown,
// not estimated. InputTokens includes cache creation and cache read tokens; the
// cache fields are subsets of that total, not additional tokens.
type Usage struct {
	ModelRequests            int64
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
}

func (u *Usage) add(other Usage) {
	u.ModelRequests += other.ModelRequests
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
}

// TokenLimits optionally bounds cumulative reported usage. Zero means unlimited.
// Input usage is only known after a response, so an individual request can
// exceed MaxInputTokens; that response fails and no further requests are sent.
// MaxOutputTokens also caps each request's max_tokens by the remaining allowance.
type TokenLimits struct {
	MaxInputTokens  int64
	MaxOutputTokens int64
}

type BudgetSnapshot struct {
	Remaining Limits
	Used      Limits
	Usage     Usage
}

// Budget shares one allowance (DefaultLimits unless set otherwise) across
// sequential completions. Concurrent Complete calls sharing it are rejected.
// Snapshots are safe during a completion. A Budget must not be copied after use.
// The zero value is equivalent to NewBudget.
type Budget struct {
	mu     sync.Mutex
	active bool
	limits Limits
	used   Limits
	usage  Usage
	tokens TokenLimits
}

func NewBudget() *Budget { return &Budget{} }

// NewBudgetWithLimits bounds the allowance with explicit limits instead of
// DefaultLimits.
func NewBudgetWithLimits(limits Limits) (*Budget, error) {
	if limits.Turns < 0 || limits.ToolCalls < 0 || limits.WebSearches < 0 {
		return nil, errors.New("model limits must not be negative")
	}
	return &Budget{limits: limits}, nil
}

func (b *Budget) max() Limits {
	if b.limits == (Limits{}) {
		return DefaultLimits
	}
	return b.limits
}

func NewBudgetWithTokenLimits(limits TokenLimits) (*Budget, error) {
	if limits.MaxInputTokens < 0 || limits.MaxOutputTokens < 0 {
		return nil, errors.New("token limits must not be negative")
	}
	return &Budget{tokens: limits}, nil
}

func (b *Budget) remaining() Limits {
	limits := b.max()
	return Limits{
		Turns:       max(0, limits.Turns-b.used.Turns),
		ToolCalls:   max(0, limits.ToolCalls-b.used.ToolCalls),
		WebSearches: max(0, limits.WebSearches-b.used.WebSearches),
	}
}

// Snapshot returns independent counts; remaining allowances never go negative,
// but Used records provider-side overruns in full.
func (b *Budget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BudgetSnapshot{Remaining: b.remaining(), Used: b.used, Usage: b.usage}
}

func (b *Budget) begin(limits *Limits) (Limits, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active {
		return Limits{}, errors.New("model budget is already in use")
	}
	if limits != nil && (limits.Turns < 0 || limits.ToolCalls < 0 || limits.WebSearches < 0) {
		return Limits{}, errors.New("model limits must not be negative")
	}
	if err := b.exceeded(); err != nil {
		return Limits{}, err
	}
	remaining := b.remaining()
	if limits != nil {
		remaining.Turns = min(remaining.Turns, limits.Turns)
		remaining.ToolCalls = min(remaining.ToolCalls, limits.ToolCalls)
		remaining.WebSearches = min(remaining.WebSearches, limits.WebSearches)
	}
	b.active = true
	return remaining, nil
}

func (b *Budget) end() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = false
}

func (b *Budget) turn(maxTokens int64) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used.Turns >= b.max().Turns {
		return 0, &IncompleteError{Reason: "model turn limit reached"}
	}
	if b.tokens.MaxInputTokens > 0 && b.usage.InputTokens >= b.tokens.MaxInputTokens {
		return 0, &IncompleteError{Reason: "input token budget reached"}
	}
	if b.tokens.MaxOutputTokens > 0 {
		remaining := b.tokens.MaxOutputTokens - b.usage.OutputTokens
		if remaining <= 0 {
			return 0, &IncompleteError{Reason: "output token budget reached"}
		}
		maxTokens = min(maxTokens, remaining)
	}
	b.used.Turns++
	b.usage.ModelRequests++
	return maxTokens, nil
}

func (b *Budget) tool() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used.ToolCalls >= b.max().ToolCalls {
		return &IncompleteError{Reason: "repository tool call limit reached"}
	}
	b.used.ToolCalls++
	return nil
}

func (b *Budget) record(usage Usage, searches int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage.add(usage)
	b.used.WebSearches += searches
	return b.exceeded()
}

func (b *Budget) exceeded() error {
	switch {
	case b.used.WebSearches > b.max().WebSearches:
		return &IncompleteError{Reason: "web search limit reached"}
	case b.tokens.MaxInputTokens > 0 && b.usage.InputTokens > b.tokens.MaxInputTokens:
		return &IncompleteError{Reason: "input token budget exceeded"}
	case b.tokens.MaxOutputTokens > 0 && b.usage.OutputTokens > b.tokens.MaxOutputTokens:
		return &IncompleteError{Reason: "output token budget exceeded"}
	default:
		return nil
	}
}
