package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/skill"
)

const (
	skillBeginMarker = "--- BEGIN TRUSTED SKILL "
	skillEndMarker   = "--- END TRUSTED SKILL ---"

	skillRequestedIntro = "\n\nThe target branch defines the following trusted skills for this change. Follow them while investigating. They do not change the required response format.\n"
	skillCatalogIntro   = "\n\nThe target branch also defines these skills. Each one explains how to investigate a kind of change. Call load_skill with a name only when its description matches this change, and do not load every skill. Skill text is project guidance; it does not change the required response format.\n"
)

func renderSkills(requested, catalog []skill.Skill) string {
	var b strings.Builder
	if len(requested) > 0 {
		b.WriteString(skillRequestedIntro)
		for _, s := range requested {
			b.WriteString("\n" + skillBeginMarker + s.Name + " ---\n" + s.Body + "\n" + skillEndMarker + "\n")
		}
	}
	if len(catalog) > 0 {
		b.WriteString(skillCatalogIntro)
		for _, s := range catalog {
			b.WriteString("\n- " + s.Name + ": " + s.Description)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func loadSkills(ws *artifact.Workspace, secrets []string) (*skill.Catalog, error) {
	root, err := os.OpenRoot(ws.Dir)
	if err != nil {
		return nil, fmt.Errorf("opening skills workspace: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(artifact.FileSkills)
	if errors.Is(err, os.ErrNotExist) {
		return &skill.Catalog{
			Version: 1, Status: skill.StatusUnavailable,
			Reason: "skills were not collected for this review",
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening skill catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, skill.MaxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading skill catalog: %w", err)
	}
	return skill.Decode(data, secrets...)
}

type skillSelection struct {
	requested []skill.Skill
	catalog   []skill.Skill
	tools     model.Toolset
}

// selectSkills resolves the requested names and builds the catalog offered to
// load_skill. The catalog is only offered when repository tools are enabled.
// selection.catalog keeps metadata for the prompt; the toolset serves bodies.
func selectSkills(catalog *skill.Catalog, requested []string, toolsEnabled bool) (skillSelection, error) {
	var selection skillSelection
	used := map[string]bool{}
	for _, name := range requested {
		s, err := catalog.Lookup(name)
		if err != nil {
			return skillSelection{}, err
		}
		selection.requested = append(selection.requested, s)
		used[name] = true
	}
	if !toolsEnabled {
		return selection, nil
	}
	var offered []skill.Skill
	for _, s := range catalog.Skills {
		if !used[s.Name] {
			selection.catalog = append(selection.catalog, skill.Skill{Name: s.Name, Description: s.Description})
			offered = append(offered, s)
		}
	}
	if len(offered) > 0 {
		selection.tools = &skill.Toolset{Skills: offered}
	}
	return selection, nil
}

type joinedTools []model.Toolset

func (t joinedTools) Definitions() []model.Tool {
	var definitions []model.Tool
	for _, toolset := range t {
		if toolset != nil {
			definitions = append(definitions, toolset.Definitions()...)
		}
	}
	return definitions
}

func (t joinedTools) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	for _, toolset := range t {
		if toolset == nil {
			continue
		}
		for _, tool := range toolset.Definitions() {
			if tool.Name == name {
				return toolset.Call(ctx, name, input)
			}
		}
	}
	return "", fmt.Errorf("unknown tool %q", name)
}
