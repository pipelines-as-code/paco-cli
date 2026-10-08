package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Evidence struct {
	Revision string `json:"revision"`
	Path     string `json:"path"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Quote    string `json:"quote"`
}

type Candidate struct {
	ID       string     `json:"id"`
	Path     string     `json:"path"`
	Line     int        `json:"line"`
	Side     string     `json:"side"`
	Severity string     `json:"severity,omitempty"`
	Claim    string     `json:"claim"`
	Trigger  string     `json:"trigger"`
	Impact   string     `json:"impact"`
	Remedy   string     `json:"remedy"`
	Evidence []Evidence `json:"evidence"`
}

type discovery struct {
	Summary           string      `json:"summary"`
	ReviewScore       ReviewScore `json:"review_score"`
	SecuritySensitive bool        `json:"security_sensitive"`
	Overflow          bool        `json:"overflow"`
	Candidates        []Candidate `json:"candidates"`
}

type decision struct {
	ID          string `json:"id"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason"`
	DuplicateOf string `json:"duplicate_of"`
	// Severity is the verifier's assessment from the confirmed impact; it
	// replaces the discovery severity of an accepted candidate.
	Severity string     `json:"severity,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

type verdict struct {
	Summary   string     `json:"summary"`
	Decisions []decision `json:"decisions"`
}

// evidenceContext includes snapshot lines and diff lines when a snapshot is absent.
// Changed lines are tracked independently: a valid citation alone cannot establish
// that a defect was introduced by this PR.
type evidenceContext struct {
	lines   map[string]map[string]map[int]string
	changed map[string]map[string]map[int]bool
}

func (c evidenceContext) validate(refs []Evidence, path string, requireChanged bool) error {
	if len(refs) == 0 || len(refs) > 12 {
		return errors.New("findings require 1 to 12 evidence references")
	}
	changed := false
	for _, ref := range refs {
		if ref.Revision != "head" && ref.Revision != "before" {
			return errors.New("evidence has an unknown revision")
		}
		if ref.Start < 1 || ref.End < ref.Start || ref.End-ref.Start >= 200 {
			return errors.New("evidence has an invalid line range")
		}
		var lines []string
		for n := ref.Start; n <= ref.End; n++ {
			line, ok := c.lines[ref.Revision][ref.Path][n]
			if !ok {
				return errors.New("evidence references unavailable source")
			}
			lines = append(lines, line)
			changed = changed || (ref.Path == path && c.changed[ref.Revision][ref.Path][n])
		}
		if ref.Quote != strings.Join(lines, "\n") {
			return errors.New("evidence quote does not match source")
		}
	}
	if requireChanged && !changed {
		return errors.New("evidence does not reference a changed line in the finding's file")
	}
	return nil
}

func (c evidenceContext) validateCandidate(v Candidate) error {
	if v.Side != "head" && v.Side != "before" {
		return errors.New("finding has an invalid side")
	}
	if !c.changed[v.Side][v.Path][v.Line] {
		return errors.New("finding anchor is not a changed line")
	}
	if !validSeverities[v.Severity] {
		return errors.New("finding has an invalid severity")
	}
	for _, value := range []string{v.ID, v.Claim, v.Trigger, v.Impact, v.Remedy} {
		if strings.TrimSpace(value) == "" || len(value) > 2000 {
			return errors.New("finding fields must contain 1 to 2000 bytes")
		}
	}
	return c.validate(v.Evidence, v.Path, true)
}

func decodeStrict(text string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid review response: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("review response must contain one JSON object")
	}
	return nil
}

func parseDiscovery(text string) (discovery, error) {
	var d discovery
	text = responseObject(text)
	if err := validateShape(text, discoverySchema()); err != nil {
		return d, err
	}
	if err := decodeStrict(text, &d); err != nil {
		return d, err
	}
	if strings.TrimSpace(d.Summary) == "" || len(d.Summary) > 6000 ||
		d.ReviewScore.Rating < 1 || d.ReviewScore.Rating > 5 ||
		strings.TrimSpace(d.ReviewScore.Reason) == "" || d.Candidates == nil || len(d.Candidates) > maxComments {
		return d, errors.New("discovery response has invalid summary, rating, or candidates")
	}
	seen := map[string]bool{}
	for _, c := range d.Candidates {
		if strings.TrimSpace(c.ID) == "" || len(c.ID) > 64 || seen[c.ID] {
			return d, errors.New("candidate IDs must be unique and contain 1 to 64 bytes")
		}
		seen[c.ID] = true
	}
	return d, nil
}

// Plain-output models sometimes put commentary before their final object,
// bare or in a JSON fence that may be left unclosed. The commentary must not
// contain JSON delimiters, so there is never a choice among competing
// objects; strict decoding then rejects anything after the object.
// responseObject strips commentary around the review object. Models asked
// for bare JSON often add prose, sometimes with brackets or code spans, and
// an optional fence before the object. The first "{" from which one complete
// JSON object parses, followed only by whitespace or a closing fence, wins.
// Ambiguous or trailing content is left for decodeStrict to reject.
func responseObject(text string) string {
	text = strings.TrimSpace(text)
	for brace := strings.IndexByte(text, '{'); brace >= 0; {
		body := text[brace:]
		if end, ok := objectEnd(body); ok {
			rest := strings.TrimSpace(body[end:])
			if rest == "" || rest == "```" {
				return body[:end]
			}
		}
		next := strings.IndexByte(text[brace+1:], '{')
		if next < 0 {
			break
		}
		brace += 1 + next
	}
	return text
}

// objectEnd returns the length of the JSON object at the start of text.
func objectEnd(text string) (int, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil || len(raw) == 0 || raw[0] != '{' {
		return 0, false
	}
	return int(decoder.InputOffset()), true
}

func parseVerdict(text string, candidates []Candidate, context evidenceContext) (verdict, error) {
	var v verdict
	text = responseObject(text)
	if err := validateShape(text, verdictSchema()); err != nil {
		return v, err
	}

	if err := decodeStrict(text, &v); err != nil {
		return v, err
	}
	if strings.TrimSpace(v.Summary) == "" || len(v.Summary) > 6000 ||
		v.Decisions == nil || len(v.Decisions) != len(candidates) {
		return v, errors.New("verifier must return a summary and one decision per candidate")
	}
	byID := map[string]Candidate{}
	for _, c := range candidates {
		byID[c.ID] = c
	}
	decisions := map[string]decision{}
	for _, d := range v.Decisions {
		c, ok := byID[d.ID]
		if !ok {
			return v, errors.New("verifier returned an unknown candidate ID")
		}
		if _, exists := decisions[d.ID]; exists {
			return v, errors.New("verifier returned a duplicate candidate ID")
		}
		if strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 2000 || d.Evidence == nil {
			return v, errors.New("verifier decision requires a reason and evidence array")
		}
		switch d.Outcome {
		case "accept":
			if d.DuplicateOf != "" {
				return v, errors.New("accepted candidate cannot be a duplicate")
			}
			if !validSeverities[d.Severity] {
				return v, errors.New("accepted candidate requires a severity")
			}
			if err := context.validate(d.Evidence, c.Path, true); err != nil {
				return v, fmt.Errorf("invalid verifier evidence: %w", err)
			}
		case "reject", "insufficient_evidence":
			if len(d.Evidence) > 0 {
				if err := context.validate(d.Evidence, c.Path, false); err != nil {
					return v, fmt.Errorf("invalid verifier evidence: %w", err)
				}
			}
			if d.Outcome == "insufficient_evidence" && d.DuplicateOf != "" {
				return v, errors.New("uncertain candidate cannot be marked duplicate")
			}
		default:
			return v, errors.New("verifier returned an unknown outcome")
		}
		decisions[d.ID] = d
	}
	for _, d := range v.Decisions {
		if d.DuplicateOf != "" {
			target, ok := decisions[d.DuplicateOf]
			if !ok || target.Outcome != "accept" || target.ID == d.ID {
				return v, errors.New("duplicate must reference an accepted candidate")
			}
		}
	}
	return v, nil
}
