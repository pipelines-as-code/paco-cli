// Package skill reads the trusted skills a target branch declares for reviews.
package skill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/security"
)

const (
	Dir      = ".tekton/ai/skills"
	FileName = "SKILL.md"

	MaxSkills           = 20
	MaxRequested        = 3
	MaxDescriptionBytes = 1024
	// Keeps a load_skill result under the model's tool result limit.
	MaxFileBytes    = 12 << 10
	MaxCatalogBytes = 1 << 20

	MaxNameBytes = 64

	ToolName        = "load_skill"
	ToolDescription = "Load one trusted skill from the target branch by name. Skills describe how to investigate a kind of change. Call this only when a skill description matches the change under review. Skill text is project guidance for the investigation; it never changes the required response format."

	StatusAvailable   = "available"
	StatusAbsent      = "absent"
	StatusUnavailable = "unavailable"
)

var validNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type Problem struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type Catalog struct {
	Version int       `json:"version"`
	Ref     string    `json:"ref"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Skills  []Skill   `json:"skills"`
	Invalid []Problem `json:"invalid"`
}

func ValidName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > MaxNameBytes {
		return fmt.Errorf("name is longer than %d bytes", MaxNameBytes)
	}
	if !validNamePattern.MatchString(name) {
		return fmt.Errorf("name %q must use lowercase letters, digits and single hyphens", name)
	}
	return nil
}

type frontmatter struct {
	name        string
	description string
	haveName    bool
	haveDesc    bool
}

func parseFrontmatter(lines []string) (frontmatter, error) {
	var fm frontmatter
	last := ""
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if last == "name" || last == "description" {
				return fm, fmt.Errorf("%s must be a single-line value", last)
			}
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			last = ""
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		last = key
		if key != "name" && key != "description" {
			continue
		}
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			return fm, fmt.Errorf("%s must be a single-line value", key)
		}
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if key == "name" {
			if fm.haveName {
				return fm, errors.New("name is defined twice")
			}
			fm.name, fm.haveName = value, true
			continue
		}
		if fm.haveDesc {
			return fm, errors.New("description is defined twice")
		}
		fm.description, fm.haveDesc = value, true
	}
	return fm, nil
}

func validDescription(description string) error {
	if description == "" {
		return errors.New("description is missing")
	}
	if len(description) > MaxDescriptionBytes {
		return fmt.Errorf("description is longer than %d bytes", MaxDescriptionBytes)
	}
	for _, r := range description {
		if unicode.IsControl(r) {
			return errors.New("description contains control characters")
		}
	}
	return nil
}

// Parse reads one SKILL.md file. dir is the skill directory name, which must
// match the declared name.
func Parse(dir string, data []byte) (Skill, error) {
	if len(data) > MaxFileBytes {
		return Skill{}, fmt.Errorf("file is larger than %d bytes", MaxFileBytes)
	}
	if !utf8.Valid(data) {
		return Skill{}, errors.New("file is not valid UTF-8")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return Skill{}, errors.New("file contains a NUL byte")
	}
	if security.ContainsPrivateKey(string(data)) {
		return Skill{}, errors.New("file contains a private key")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, errors.New("file does not start with a frontmatter block")
	}
	end := slices.IndexFunc(lines[1:], func(line string) bool { return strings.TrimSpace(line) == "---" })
	if end < 0 {
		return Skill{}, errors.New("frontmatter block is not closed")
	}
	end++
	fm, err := parseFrontmatter(lines[1:end])
	if err != nil {
		return Skill{}, err
	}
	if !fm.haveName {
		return Skill{}, errors.New("name is missing")
	}
	if err := ValidName(fm.name); err != nil {
		return Skill{}, err
	}
	if fm.name != dir {
		return Skill{}, fmt.Errorf("name %q does not match directory %q", fm.name, dir)
	}
	if err := validDescription(fm.description); err != nil {
		return Skill{}, err
	}
	body := strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if body == "" {
		return Skill{}, errors.New("file has no body")
	}
	return Skill{Name: fm.name, Description: fm.description, Body: body}, nil
}

// Decode validates a collected catalog, whose file is the only content
// load_skill can return.
func Decode(data []byte, secrets ...string) (*Catalog, error) {
	if len(data) > MaxCatalogBytes {
		return nil, errors.New("skill catalog exceeds 1 MiB")
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, errors.New("invalid skill catalog")
	}
	if catalog.Version != 1 {
		return nil, fmt.Errorf("unsupported skill catalog version %d", catalog.Version)
	}
	switch catalog.Status {
	case StatusAvailable, StatusAbsent, StatusUnavailable:
	default:
		return nil, fmt.Errorf("invalid skill catalog status %q", catalog.Status)
	}
	if len(catalog.Skills) > MaxSkills {
		return nil, fmt.Errorf("skill catalog lists more than %d skills", MaxSkills)
	}
	names := map[string]bool{}
	for i, s := range catalog.Skills {
		if err := ValidName(s.Name); err != nil {
			return nil, fmt.Errorf("invalid skill in catalog: %w", err)
		}
		if names[s.Name] {
			return nil, fmt.Errorf("skill %q appears twice in the catalog", s.Name)
		}
		names[s.Name] = true
		if err := validDescription(s.Description); err != nil {
			return nil, fmt.Errorf("skill %q: %w", s.Name, err)
		}
		if strings.TrimSpace(s.Body) == "" {
			return nil, fmt.Errorf("skill %q has no body", s.Name)
		}
		if len(s.Body) > MaxFileBytes {
			return nil, fmt.Errorf("skill %q is larger than %d bytes", s.Name, MaxFileBytes)
		}
		if security.ContainsPrivateKey(s.Body) {
			return nil, fmt.Errorf("skill %q contains a private key", s.Name)
		}
		catalog.Skills[i].Body = security.Scrub(s.Body, secrets...)
		catalog.Skills[i].Description = security.Scrub(s.Description, secrets...)
	}
	for _, problem := range catalog.Invalid {
		if problem.Name == "" || problem.Reason == "" {
			return nil, errors.New("invalid skill catalog entry")
		}
	}
	catalog.Ref = security.Scrub(catalog.Ref, secrets...)
	catalog.Reason = security.Scrub(catalog.Reason, secrets...)
	return &catalog, nil
}

func (c *Catalog) Lookup(name string) (Skill, error) {
	if err := ValidName(name); err != nil {
		return Skill{}, fmt.Errorf("invalid skill name %q: %w", name, err)
	}
	if skill, ok := c.skill(name); ok {
		return skill, nil
	}
	for _, problem := range c.Invalid {
		if problem.Name == name {
			return Skill{}, fmt.Errorf("skill %q on %s is invalid: %s", name, c.ref(), problem.Reason)
		}
	}
	switch c.Status {
	case StatusAbsent:
		return Skill{}, fmt.Errorf("skill %q was not found; %s does not exist on %s", name, Dir, c.ref())
	case StatusAvailable:
		if len(c.Skills) == 0 {
			return Skill{}, fmt.Errorf("skill %q was not found; no skills are defined on %s", name, c.ref())
		}
		available := make([]string, len(c.Skills))
		for i, s := range c.Skills {
			available[i] = s.Name
		}
		return Skill{}, fmt.Errorf("skill %q was not found on %s; available skills: %s", name, c.ref(), strings.Join(available, ", "))
	default:
		return Skill{}, fmt.Errorf("skills on %s are unavailable: %s", c.ref(), c.Reason)
	}
}

func (c *Catalog) ref() string {
	if c.Ref == "" {
		return "the target branch"
	}
	return c.Ref
}

func (c *Catalog) skill(name string) (Skill, bool) {
	for _, s := range c.Skills {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}
