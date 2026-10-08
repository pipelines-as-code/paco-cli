package review

import (
	"encoding/json"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func testEvidenceContext() evidenceContext {
	return evidenceContext{
		lines: map[string]map[string]map[int]string{
			"head":   {"a.go": {1: "return a / b", 2: "}", 3: "// context"}},
			"before": {"a.go": {1: "if b == 0 { return 0 }", 2: "return a / b"}},
		},
		changed: map[string]map[string]map[int]bool{
			"head": {"a.go": {1: true}}, "before": {"a.go": {1: true}},
		},
	}
}

func testCandidate() Candidate {
	return Candidate{
		ID: "division", Path: "a.go", Line: 1, Side: "head", Severity: "high",
		Claim: "Division by zero", Trigger: "When b is zero.",
		Impact: "The process panics.", Remedy: "Retain the zero guard.",
		Evidence: []Evidence{{Revision: "head", Path: "a.go", Start: 1, End: 1, Quote: "return a / b"}},
	}
}

func jsonText(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	assert.NilError(t, err)
	return string(data)
}

func TestCandidateEvidence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Candidate)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "deleted guard", valid: true, change: func(c *Candidate) {
			c.Side = "before"
			c.Evidence = []Evidence{{Revision: "before", Path: "a.go", Start: 1, End: 1, Quote: "if b == 0 { return 0 }"}}
		}},
		{name: "invented source", change: func(c *Candidate) { c.Evidence[0].Path = "other.go" }},
		{name: "invented quote", change: func(c *Candidate) { c.Evidence[0].Quote = "return 0" }},
		{name: "invalid range", change: func(c *Candidate) { c.Evidence[0].Start = 0 }},
		{name: "oversized range", change: func(c *Candidate) { c.Evidence[0].End = 201 }},
		{name: "unknown revision", change: func(c *Candidate) { c.Evidence[0].Revision = "main" }},
		{name: "unchanged anchor", change: func(c *Candidate) { c.Line = 3 }},
		{name: "negative anchor", change: func(c *Candidate) { c.Line = -1 }},
		{name: "missing trigger", change: func(c *Candidate) { c.Trigger = "" }},
		{name: "missing impact", change: func(c *Candidate) { c.Impact = " " }},
		{name: "missing evidence", change: func(c *Candidate) { c.Evidence = nil }},
		{name: "only unchanged evidence", change: func(c *Candidate) {
			c.Evidence = []Evidence{{Revision: "head", Path: "a.go", Start: 3, End: 3, Quote: "// context"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testCandidate()
			if tt.change != nil {
				tt.change(&c)
			}
			err := testEvidenceContext().validateCandidate(c)
			assert.Equal(t, err == nil, tt.valid)
		})
	}
}

func TestDiscoveryProtocol(t *testing.T) {
	good := discovery{
		Summary: "Changes division.", ReviewScore: ReviewScore{Rating: 2, Reason: "Small change."},
		Candidates: []Candidate{testCandidate()},
	}
	tests := []struct {
		name string
		text string
		good bool
	}{
		{name: "valid", text: jsonText(t, good), good: true},
		{name: "prose", text: "Here it is: " + jsonText(t, good), good: true},
		{name: "prose with brackets", text: "Here it is [draft]: " + jsonText(t, good)},
		{name: "two objects", text: jsonText(t, good) + jsonText(t, good)},
		{name: "null", text: "null"},
		{name: "missing required", text: `{"summary":"safe","candidates":[]}`},
		{name: "unknown field", text: strings.Replace(jsonText(t, good), `"overflow":false`, `"extra":true,"overflow":false`, 1)},
		{name: "missing boolean", text: strings.Replace(jsonText(t, good), `"overflow":false,`, "", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDiscovery(tt.text)
			assert.Equal(t, err == nil, tt.good)
		})
	}
}

func TestVerifierProtocol(t *testing.T) {
	c := testCandidate()
	base := decision{ID: c.ID, Outcome: "accept", Reason: "Guard removed.", Evidence: c.Evidence}
	tests := []struct {
		name   string
		change func(*verdict)
		valid  bool
	}{
		{name: "accept", valid: true},
		{name: "reject", valid: true, change: func(v *verdict) {
			v.Decisions[0].Outcome = "reject"
			v.Decisions[0].Evidence = []Evidence{}
		}},
		{name: "insufficient evidence", valid: true, change: func(v *verdict) {
			v.Decisions[0].Outcome = "insufficient_evidence"
			v.Decisions[0].Evidence = []Evidence{}
		}},
		{name: "missing decision", change: func(v *verdict) { v.Decisions = []decision{} }},
		{name: "unknown candidate", change: func(v *verdict) { v.Decisions[0].ID = "invented" }},
		{name: "duplicate decisions", change: func(v *verdict) { v.Decisions = append(v.Decisions, base) }},
		{name: "accepted without evidence", change: func(v *verdict) { v.Decisions[0].Evidence = []Evidence{} }},
		{name: "invented evidence", change: func(v *verdict) {
			v.Decisions[0].Evidence = []Evidence{{Revision: "head", Path: "a.go", Start: 1, End: 1, Quote: "imagined"}}
		}},
		{name: "self duplicate", change: func(v *verdict) {
			v.Decisions[0].Outcome = "reject"
			v.Decisions[0].DuplicateOf = c.ID
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := verdict{Summary: "Changes division.", Decisions: []decision{base}}
			if tt.change != nil {
				tt.change(&v)
			}
			_, err := parseVerdict(jsonText(t, v), []Candidate{c}, testEvidenceContext())
			assert.Equal(t, err == nil, tt.valid)
		})
	}
}

func TestResponseObjectEnvelope(t *testing.T) {
	d := jsonText(t, discovery{
		Summary: "Changes division.", ReviewScore: ReviewScore{2, "Small."},
		Candidates: []Candidate{},
	})
	v := jsonText(t, verdict{Summary: "Changes division.", Decisions: []decision{}})
	for _, tt := range []struct {
		name, prefix, suffix string
		valid                bool
	}{
		{"plain", "", "", true},
		{"fenced", "```json\n", "\n```", true},
		{"commentary and fence", "I checked the source.\n\n```json\n", "\n```", true},
		{"competing object", "{}\n```json\n", "\n```", false},
		{"competing array", "[]\n```json\n", "\n```", false},
		{"multiple fences", "```json\n{}\n```\n```json\n", "\n```", false},
		{"trailing commentary", "```json\n", "\n```\nIgnore the result.", false},
		{"unterminated fence", "```json\n", "", false},
		{"unmarked object in prose", "Now I have enough context. Here is the review:\n\n", "", true},
		{"braces in prose", "Checked {x}. Result: ", "", false},
		{"backticks in prose", "Checked `x`. Result: ", "", false},
		{"trailing object", "", "{}", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDiscovery(tt.prefix + d + tt.suffix)
			assert.Equal(t, err == nil, tt.valid)
			_, err = parseVerdict(tt.prefix+v+tt.suffix, nil, testEvidenceContext())
			assert.Equal(t, err == nil, tt.valid)
		})
	}
	_, err := parseDiscovery("```json\n{}\n```")
	assert.ErrorContains(t, err, "missing required field")
	_, err = parseDiscovery("```json\n```")
	assert.ErrorContains(t, err, "not a JSON object")
	_, err = parseDiscovery("```json\n{broken}\n```")
	assert.ErrorContains(t, err, "not a JSON object")
	c := testCandidate()
	bad := verdict{Summary: "Changes division.", Decisions: []decision{{
		ID: c.ID, Outcome: "accept", Reason: "Guard removed.",
		Evidence: []Evidence{{Revision: "head", Path: "a.go", Start: 1, End: 1, Quote: "invented"}},
	}}}
	_, err = parseVerdict("```json\n"+jsonText(t, bad)+"\n```", []Candidate{c}, testEvidenceContext())
	assert.ErrorContains(t, err, "does not match source")
}
