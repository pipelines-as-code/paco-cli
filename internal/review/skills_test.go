package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
	"github.com/pipelines-as-code/paco-cli/internal/skill"
	"gotest.tools/v3/assert"
)

var testSkills = []skill.Skill{
	{Name: "alpha", Description: "First skill", Body: "Check the alpha path."},
	{Name: "beta", Description: "Second skill", Body: "Check the beta path."},
}

func writeSkills(t *testing.T, ws, data string) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileSkills), []byte(data), 0o600))
}

func writeSkillsCatalog(t *testing.T, ws string, skills ...skill.Skill) {
	t.Helper()
	data, err := json.Marshal(skill.Catalog{
		Version: 1, Ref: "main", Status: skill.StatusAvailable,
		Skills: skills, Invalid: []skill.Problem{},
	})
	assert.NilError(t, err)
	writeSkills(t, ws, string(data))
}

func TestSelectSkills(t *testing.T) {
	catalog := &skill.Catalog{
		Version: 1, Ref: "main", Status: skill.StatusAvailable,
		Skills: []skill.Skill{
			{Name: "alpha", Description: "First skill", Body: "a"},
			{Name: "beta", Description: "Second skill", Body: "b"},
		},
	}

	selection, err := selectSkills(catalog, nil, true)
	assert.NilError(t, err)
	assert.Equal(t, len(selection.requested), 0)
	assert.Equal(t, len(selection.catalog), 2)
	assert.Equal(t, len(selection.tools.Definitions()), 1)

	selection, err = selectSkills(catalog, []string{"alpha"}, true)
	assert.NilError(t, err)
	assert.Equal(t, selection.requested[0].Body, "a")
	assert.DeepEqual(t, selection.catalog, []skill.Skill{{Name: "beta", Description: "Second skill"}})
	out, err := selection.tools.Call(context.Background(), skill.ToolName, json.RawMessage(`{"name":"beta"}`))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "b"), out)
	assert.Assert(t, !strings.Contains(out, "Check the alpha path."), out)

	selection, err = selectSkills(catalog, nil, false)
	assert.NilError(t, err)
	assert.Equal(t, len(selection.catalog), 0)
	assert.Assert(t, selection.tools == nil)

	selection, err = selectSkills(catalog, []string{"alpha", "beta"}, true)
	assert.NilError(t, err)
	assert.Equal(t, len(selection.requested), 2)
	assert.Assert(t, selection.tools == nil)

	_, err = selectSkills(catalog, []string{"gamma"}, true)
	assert.ErrorContains(t, err, `skill "gamma" was not found on main; available skills: alpha, beta`)

	_, err = selectSkills(&skill.Catalog{Ref: "main", Status: skill.StatusUnavailable, Reason: "not collected"}, []string{"alpha"}, true)
	assert.ErrorContains(t, err, "unavailable: not collected")
}

func TestLoadSkills(t *testing.T) {
	ws := &artifact.Workspace{Dir: t.TempDir()}

	catalog, err := loadSkills(ws, nil)
	assert.NilError(t, err)
	assert.Equal(t, catalog.Status, skill.StatusUnavailable)
	_, err = catalog.Lookup("alpha")
	assert.ErrorContains(t, err, "not collected")

	writeSkillsCatalog(t, ws.Dir, testSkills...)
	catalog, err = loadSkills(ws, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(catalog.Skills), 2)

	writeSkills(t, ws.Dir, "{")
	_, err = loadSkills(ws, nil)
	assert.ErrorContains(t, err, "invalid skill catalog")
}

func TestJoinedTools(t *testing.T) {
	source := testToolset{name: "read_file"}
	skills := &skill.Toolset{Skills: testSkills}
	joined := joinedTools{source, nil, skills}

	names := map[string]bool{}
	for _, tool := range joined.Definitions() {
		names[tool.Name] = true
	}
	assert.Assert(t, names["read_file"] && names[skill.ToolName])

	out, err := joined.Call(context.Background(), skill.ToolName, json.RawMessage(`{"name":"alpha"}`))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "Check the alpha path."))

	out, err = joined.Call(context.Background(), "read_file", nil)
	assert.NilError(t, err)
	assert.Equal(t, out, "read_file")

	_, err = joined.Call(context.Background(), "unknown", nil)
	assert.ErrorContains(t, err, `unknown tool "unknown"`)
}

type testToolset struct{ name string }

func (t testToolset) Definitions() []model.Tool {
	return []model.Tool{{Name: t.name, Description: "test tool"}}
}

func (t testToolset) Call(context.Context, string, json.RawMessage) (string, error) {
	return t.name, nil
}

func skillsWorkspace(t *testing.T) string {
	t.Helper()
	ws := setupWorkspaceWithDiff(t, "diff")
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileHeadSHA), []byte("sha\n"), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileSource),
		[]byte(`{"commit":"sha","files":{"a.go":"package a"}}`), 0o600))
	return ws
}

func toolNames(tools model.Toolset) []string {
	var names []string
	if tools == nil {
		return names
	}
	for _, tool := range tools.Definitions() {
		names = append(names, tool.Name)
	}
	return names
}

func TestRunSkills(t *testing.T) {
	tests := []struct {
		name       string
		comment    string
		disabled   bool
		skills     []skill.Skill
		wantTools  []string
		wantPrompt []string
		notPrompt  []string
	}{
		{
			name:       "catalog is offered with repository tools",
			skills:     testSkills,
			wantTools:  []string{"list_files", "read_file", "search_code", skill.ToolName},
			wantPrompt: []string{"also defines these skills", "- alpha: First skill", "- beta: Second skill", "Call load_skill"},
		},
		{
			name:      "no catalog without repository tools",
			disabled:  true,
			skills:    testSkills,
			notPrompt: []string{"also defines these skills", "BEGIN TRUSTED SKILL"},
		},
		{
			name:       "requested skill is included in full and not offered",
			comment:    "/paco review skill:alpha",
			skills:     testSkills,
			wantTools:  []string{"list_files", "read_file", "search_code", skill.ToolName},
			wantPrompt: []string{"--- BEGIN TRUSTED SKILL alpha ---", "Check the alpha path.", "- beta: Second skill"},
			notPrompt:  []string{"- alpha: First skill"},
		},
		{
			name:       "requested skill survives disabled exploration",
			comment:    "/paco review skill:alpha",
			disabled:   true,
			skills:     testSkills,
			wantPrompt: []string{"--- BEGIN TRUSTED SKILL alpha ---", "Check the alpha path."},
			notPrompt:  []string{"also defines these skills"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := skillsWorkspace(t)
			writeSkillsCatalog(t, ws, tt.skills...)
			fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
			assert.NilError(t, Run(context.Background(), Options{
				Workspace: ws, TriggerComment: tt.comment, Resolve: fakeResolve(fc), NoExploration: tt.disabled,
			}))
			assert.Assert(t, !fileExists(filepath.Join(ws, artifact.FileFailed)))
			if tt.wantTools != nil {
				assert.DeepEqual(t, toolNames(fc.got.Tools), tt.wantTools)
			}
			if tt.disabled {
				assert.Assert(t, fc.got.Tools == nil)
			}
			for _, text := range tt.wantPrompt {
				assert.Assert(t, strings.Contains(fc.got.Prompt, text), "prompt missing %q", text)
			}
			for _, text := range tt.notPrompt {
				assert.Assert(t, !strings.Contains(fc.got.Prompt, text), "prompt contains %q", text)
			}
		})
	}
}

func TestRunSkillsFailures(t *testing.T) {
	tests := []struct {
		name    string
		comment string
		catalog string
		want    string
	}{
		{
			name:    "requested skill is missing",
			comment: "/paco review skill:gamma",
			catalog: `{"version":1,"ref":"main","status":"available","skills":[{"name":"alpha","description":"First","body":"a"}],"invalid":[]}`,
			want:    `skill "gamma" was not found on main; available skills: alpha`,
		},
		{
			name:    "requested skill is invalid",
			comment: "/paco review skill:broken",
			catalog: `{"version":1,"ref":"main","status":"available","skills":[],"invalid":[{"name":"broken","reason":"description is missing"}]}`,
			want:    `skill "broken" on main is invalid: description is missing`,
		},
		{
			name:    "skills were never collected",
			comment: "/paco review skill:alpha",
			want:    "skills on the target branch are unavailable",
		},
		{
			name:    "corrupt catalog",
			comment: "",
			catalog: "{",
			want:    "invalid skill catalog",
		},
		{
			name:    "invalid skill name",
			comment: "/paco review skill:Bad_Name",
			catalog: `{"version":1,"ref":"main","status":"available","skills":[],"invalid":[]}`,
			want:    "invalid skill name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := skillsWorkspace(t)
			if tt.catalog != "" {
				writeSkills(t, ws, tt.catalog)
			}
			fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
			assert.NilError(t, Run(context.Background(), Options{
				Workspace: ws, TriggerComment: tt.comment, Resolve: fakeResolve(fc),
			}))
			assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
			assert.Equal(t, fc.calls, 0, "no model call may happen on a skill failure")
			assert.Assert(t, strings.Contains(readReview(t, ws).Summary, tt.want), readReview(t, ws).Summary)
		})
	}
}

func TestRunSkillsPromptUnchangedWithoutSkills(t *testing.T) {
	ws := skillsWorkspace(t)
	writeSkillsCatalog(t, ws)
	fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: fakeResolve(fc)}))
	fcPlain := &fakeClient{text: `{"summary":"ok","comments":[]}`}
	assert.NilError(t, Run(context.Background(), Options{Workspace: skillsWorkspace(t), Resolve: fakeResolve(fcPlain)}))
	assert.Equal(t, fc.got.Prompt, fcPlain.got.Prompt)
	assert.DeepEqual(t, toolNames(fc.got.Tools), toolNames(fcPlain.got.Tools))
}

func TestRunSkillsLoadRejectsHeadOnlySkill(t *testing.T) {
	ws := skillsWorkspace(t)
	assert.NilError(t, os.WriteFile(filepath.Join(ws, artifact.FileSource), []byte(`{"commit":"sha","files":{"a.go":"package a",".tekton/ai/skills/evil/SKILL.md":"---\nname: evil\ndescription: from head\n---\n\nhead body\n"}}`), 0o600))
	writeSkillsCatalog(t, ws, skill.Skill{Name: "alpha", Description: "First skill", Body: "Check alpha"})

	fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: fakeResolve(fc)}))
	assert.Assert(t, fc.got.Tools != nil)
	_, err := fc.got.Tools.Call(context.Background(), skill.ToolName, json.RawMessage(`{"name":"evil"}`))
	assert.ErrorContains(t, err, `unknown skill "evil"; available skills: alpha`)
	assert.Assert(t, !strings.Contains(fc.got.Prompt, "head body"), fc.got.Prompt)
}

func TestRunSkillsLoadReturnsBody(t *testing.T) {
	ws := skillsWorkspace(t)
	writeSkillsCatalog(t, ws, testSkills...)

	fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
	assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: fakeResolve(fc)}))
	assert.Assert(t, strings.Contains(fc.got.Prompt, "- beta: Second skill"), fc.got.Prompt)
	out, err := fc.got.Tools.Call(context.Background(), skill.ToolName, json.RawMessage(`{"name":"beta"}`))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "Check the beta path."), out)
}

func TestSystemPromptSkillException(t *testing.T) {
	t.Run("single pass", func(t *testing.T) {
		ws := skillsWorkspace(t)
		writeSkillsCatalog(t, ws, testSkills...)
		fc := &fakeClient{text: `{"summary":"ok","comments":[]}`}
		assert.NilError(t, Run(context.Background(), Options{Workspace: ws, Resolve: fakeResolve(fc)}))
		for _, want := range []string{"returned by load_skill", "it is investigation guidance", "remain untrusted data"} {
			assert.Assert(t, strings.Contains(fc.got.System, want), "system prompt missing %q", want)
		}
	})
	t.Run("verifier", func(t *testing.T) {
		ws := verifiedWorkspace(t)
		writeSkillsCatalog(t, ws, testSkills...)
		c := deletedCandidate()
		resolve, requests := reviewServer(t, []string{
			jsonText(t, discovery{Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{c}}),
			jsonText(t, verdict{Summary: "Removes a guard.", Decisions: []decision{{ID: c.ID, Outcome: "reject", Reason: "Not a defect."}}}),
		})
		assert.NilError(t, Run(context.Background(), Options{Workspace: ws, VerifyFindings: true, Resolve: resolve}))
		assert.Assert(t, len(*requests) == 2, "expected discovery and verification")
		system := jsonText(t, (*requests)[1]["system"])
		for _, want := range []string{"returned by load_skill", "it is investigation guidance", "remain untrusted."} {
			assert.Assert(t, strings.Contains(system, want), "verifier system prompt missing %q", want)
		}
	})
}

func TestVerifiedSkills(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		ws := verifiedWorkspace(t)
		writeSkillsCatalog(t, ws, testSkills...)
		resolve, requests := reviewServer(t, []string{jsonText(t, discovery{
			Summary: "Removes a guard.", ReviewScore: ReviewScore{2, "Small."}, Candidates: []Candidate{},
		})})
		assert.NilError(t, Run(context.Background(), Options{
			Workspace: ws, VerifyFindings: true, NoExploration: disabled, Resolve: resolve,
			TriggerComment: "/paco review skill:alpha",
		}))
		assert.Equal(t, len(*requests), 1)
		messages := jsonText(t, (*requests)[0]["messages"])
		assert.Assert(t, strings.Contains(messages, "--- BEGIN TRUSTED SKILL alpha ---"), messages)
		assert.Assert(t, strings.Contains(messages, "Check the alpha path."), messages)
		names := map[string]bool{}
		tools, _ := (*requests)[0]["tools"].([]any)
		for _, tool := range tools {
			names[tool.(map[string]any)["name"].(string)] = true
		}
		assert.Equal(t, names[skill.ToolName], !disabled)
		if disabled {
			assert.Equal(t, len(names), 1, "only the status tool remains")
		}
	}
}

func TestVerifiedMissingRequestedSkillFailsBeforeModel(t *testing.T) {
	ws := verifiedWorkspace(t)
	writeSkillsCatalog(t, ws, testSkills...)
	resolve, requests := reviewServer(t, nil)
	assert.NilError(t, Run(context.Background(), Options{
		Workspace: ws, VerifyFindings: true, Resolve: resolve,
		TriggerComment: "/paco review skill:gamma",
	}))
	assert.Equal(t, len(*requests), 0)
	assert.Assert(t, fileExists(filepath.Join(ws, artifact.FileFailed)))
	assert.Assert(t, strings.Contains(readReview(t, ws).Summary, `skill "gamma" was not found on main`), readReview(t, ws).Summary)
}
