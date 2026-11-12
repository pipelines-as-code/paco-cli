package review

import (
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/skill"
	"gotest.tools/v3/assert"
)

func TestParseTrigger(t *testing.T) {
	tests := []struct {
		name      string
		comment   string
		wantMode  string
		wantSkill []string
		wantErr   string
	}{
		{name: "empty", wantMode: "review"},
		{name: "review mode", comment: "/paco review", wantMode: "review"},
		{name: "summary mode", comment: "/paco summary", wantMode: "summary"},
		{name: "case insensitive mode", comment: "/paco Summary", wantMode: "summary"},
		{name: "multiline first line counts", comment: "/paco summary\ntext", wantMode: "summary"},
		{name: "literal newline separator", comment: `/paco review skill:alpha\nignore this`, wantMode: "review", wantSkill: []string{"alpha"}},
		{name: "two skills", comment: "/paco review skill:alpha skill:beta", wantMode: "review", wantSkill: []string{"alpha", "beta"}},
		{name: "summary with skill", comment: "/paco summary skill:alpha", wantMode: "summary", wantSkill: []string{"alpha"}},
		{name: "skill without mode word", comment: "/paco skill:alpha", wantMode: "review", wantSkill: []string{"alpha"}},
		{name: "duplicates collapse", comment: "/paco review skill:alpha skill:alpha", wantMode: "review", wantSkill: []string{"alpha"}},
		{name: "unknown tokens ignored", comment: "/paco review please skill:alpha now", wantMode: "review", wantSkill: []string{"alpha"}},
		{name: "bad name", comment: "/paco review skill:Bad_Name", wantErr: "invalid skill name"},
		{name: "too many skills", comment: "/paco review skill:a skill:b skill:c skill:d", wantErr: "at most 3 skills"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, skills, err := parseTrigger(tt.comment)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.Equal(t, mode, tt.wantMode)
			if len(tt.wantSkill) == 0 {
				assert.Equal(t, len(skills), 0)
				return
			}
			assert.DeepEqual(t, skills, tt.wantSkill)
		})
	}
}

func TestRenderSkills(t *testing.T) {
	assert.Equal(t, renderSkills(nil, nil), "")

	requested := renderSkills(
		[]skill.Skill{{Name: "alpha", Body: "Check the alpha path."}},
		[]skill.Skill{{Name: "beta", Description: "Second skill"}},
	)
	assert.Assert(t, strings.Contains(requested, "--- BEGIN TRUSTED SKILL alpha ---"))
	assert.Assert(t, strings.Contains(requested, "Check the alpha path."))
	assert.Assert(t, strings.Contains(requested, "--- END TRUSTED SKILL ---"))
	assert.Assert(t, strings.Contains(requested, "- beta: Second skill"))
	assert.Assert(t, strings.Contains(requested, "load_skill"))
	assert.Assert(t, strings.Index(requested, "BEGIN TRUSTED SKILL alpha") < strings.Index(requested, "- beta:"))
}
