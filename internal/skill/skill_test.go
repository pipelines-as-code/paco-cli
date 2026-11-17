package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestValidName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "a", ok: true},
		{name: "diagnose-pipelinerun-failure", ok: true},
		{name: strings.Repeat("a", 64), ok: true},
		{name: ""},
		{name: strings.Repeat("a", 65)},
		{name: "Upper"},
		{name: "under_score"},
		{name: "-leading"},
		{name: "trailing-"},
		{name: "double--hyphen"},
		{name: ".."},
		{name: "../x"},
		{name: "café"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidName(tt.name)
			assert.Equal(t, err == nil, tt.ok, err)
		})
	}
}

func skillFile(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
}

func TestParse(t *testing.T) {
	const valid = "name: demo\ndescription: A demo skill\n---\n\nInvestigate the demo.\n"
	tests := []struct {
		name    string
		dir     string
		data    string
		want    Skill
		wantErr string
	}{
		{
			name: "minimal",
			dir:  "demo",
			data: "---\n" + valid,
			want: Skill{Name: "demo", Description: "A demo skill", Body: "Investigate the demo."},
		},
		{
			name: "crlf is normalized",
			dir:  "demo",
			data: "---\r\n" + strings.ReplaceAll(valid, "\n", "\r\n"),
			want: Skill{Name: "demo", Description: "A demo skill", Body: "Investigate the demo."},
		},
		{
			name: "quoted values and unknown keys",
			dir:  "demo",
			data: "---\nname: \"demo\"\ndescription: 'A demo skill'\nlicense: MIT\nmetadata:\n  author: someone\n  version: 1\n---\n\nInvestigate the demo.\n",
			want: Skill{Name: "demo", Description: "A demo skill", Body: "Investigate the demo."},
		},
		{name: "no frontmatter", dir: "demo", data: "Investigate the demo.\n", wantErr: "does not start with a frontmatter"},
		{name: "unclosed frontmatter", dir: "demo", data: "---\nname: demo\ndescription: x\n", wantErr: "not closed"},
		{name: "missing name", dir: "demo", data: "---\ndescription: A demo skill\n---\nbody\n", wantErr: "name is missing"},
		{name: "name does not match directory", dir: "other", data: "---\n" + valid, wantErr: "does not match directory"},
		{name: "missing description", dir: "demo", data: "---\nname: demo\n---\nbody\n", wantErr: "description is missing"},
		{
			name:    "description too long",
			dir:     "demo",
			data:    "---\nname: demo\ndescription: " + strings.Repeat("a", 1025) + "\n---\nbody\n",
			wantErr: "longer than 1024 bytes",
		},
		{name: "block description", dir: "demo", data: "---\nname: demo\ndescription: >\n  folded\n---\nbody\n", wantErr: "single-line value"},
		{
			name:    "multiline description",
			dir:     "demo",
			data:    "---\nname: demo\ndescription: Investigate\n  the demo.\n---\nbody\n",
			wantErr: "description must be a single-line value",
		},
		{
			name:    "indented name continuation",
			dir:     "demo",
			data:    "---\nname: demo\n  demo-2\ndescription: x\n---\nbody\n",
			wantErr: "name must be a single-line value",
		},
		{name: "duplicate name", dir: "demo", data: "---\nname: demo\nname: demo\ndescription: x\n---\nbody\n", wantErr: "defined twice"},
		{name: "duplicate description", dir: "demo", data: "---\nname: demo\ndescription: x\ndescription: y\n---\nbody\n", wantErr: "defined twice"},
		{name: "empty body", dir: "demo", data: "---\nname: demo\ndescription: x\n---\n\n", wantErr: "no body"},
		{
			name:    "file too large",
			dir:     "demo",
			data:    skillFile("demo", "A demo skill", strings.Repeat("a", MaxFileBytes)),
			wantErr: "larger than",
		},
		{name: "invalid utf8", dir: "demo", data: "---\nname: demo\ndescription: \xff\n---\nbody\n", wantErr: "not valid UTF-8"},
		{name: "nul byte", dir: "demo", data: "---\nname: demo\ndescription: x\n---\nbody\x00\n", wantErr: "NUL byte"},
		{name: "private key", dir: "demo", data: "---\nname: demo\ndescription: x\n---\n-----BEGIN RSA PRIVATE KEY-----\n", wantErr: "private key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.dir, []byte(tt.data))
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func validCatalog(t *testing.T, catalog Catalog) []byte {
	t.Helper()
	if catalog.Version == 0 {
		catalog.Version = 1
	}
	if catalog.Status == "" {
		catalog.Status = StatusAvailable
	}
	data, err := json.Marshal(catalog)
	assert.NilError(t, err)
	return data
}

func TestDecode(t *testing.T) {
	valid := validCatalog(t, Catalog{
		Ref:    "def456",
		Skills: []Skill{{Name: "demo", Description: "A demo skill", Body: "Investigate the demo."}},
	})

	tests := []struct {
		name    string
		data    string
		wantErr string
		check   func(t *testing.T, c *Catalog)
	}{
		{name: "valid", data: string(valid), check: func(t *testing.T, c *Catalog) {
			assert.Equal(t, c.Ref, "def456")
			assert.Equal(t, c.Skills[0].Body, "Investigate the demo.")
		}},
		{name: "not json", data: "{", wantErr: "invalid skill catalog"},
		{name: "wrong version", data: `{"version":2,"status":"available","skills":[]}`, wantErr: "unsupported skill catalog version"},
		{name: "invalid status", data: `{"version":1,"status":"unknown","skills":[]}`, wantErr: "invalid skill catalog status"},
		{name: "oversized", data: strings.Repeat("x", MaxCatalogBytes+1), wantErr: "exceeds 4 MiB"},
		{
			name:    "tampered name",
			data:    `{"version":1,"status":"available","skills":[{"name":"../x","description":"d","body":"b"}]}`,
			wantErr: "invalid skill in catalog",
		},
		{
			name:    "empty body",
			data:    `{"version":1,"status":"available","skills":[{"name":"demo","description":"d","body":" "}]}`,
			wantErr: "has no body",
		},
		{
			name:    "too many skills",
			data:    `{"version":1,"status":"available","skills":[` + strings.Repeat(`{"name":"a","description":"d","body":"b"},`, MaxSkills) + `{"name":"b","description":"d","body":"b"}]}`,
			wantErr: "more than",
		},
		{
			name:    "invalid problem entry",
			data:    `{"version":1,"status":"available","skills":[],"invalid":[{"name":"x"}]}`,
			wantErr: "invalid skill catalog entry",
		},
		{
			name: "secrets are scrubbed",
			data: `{"version":1,"status":"available","ref":"main","skills":[{"name":"demo","description":"for literal-secret","body":"use literal-secret"}],"invalid":[{"name":"bad","reason":"saw literal-secret"}]}`,
			check: func(t *testing.T, c *Catalog) {
				assert.Assert(t, !strings.Contains(c.Skills[0].Body, "literal-secret"))
				assert.Assert(t, !strings.Contains(c.Skills[0].Description, "literal-secret"))
				assert.Assert(t, !strings.Contains(c.Invalid[0].Reason, "literal-secret"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog, err := Decode([]byte(tt.data), "literal-secret")
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NilError(t, err)
			if tt.check != nil {
				tt.check(t, catalog)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	available := &Catalog{Ref: "main", Status: StatusAvailable, Skills: []Skill{
		{Name: "alpha", Description: "first", Body: "a"},
		{Name: "beta", Description: "second", Body: "b"},
	}, Invalid: []Problem{{Name: "broken", Reason: "description is missing"}}}

	skill, err := available.Lookup("alpha")
	assert.NilError(t, err)
	assert.Equal(t, skill.Body, "a")

	_, err = available.Lookup("broken")
	assert.ErrorContains(t, err, "is invalid: description is missing")

	_, err = available.Lookup("gamma")
	assert.ErrorContains(t, err, `skill "gamma" was not found on main; available skills: alpha, beta`)

	_, err = (&Catalog{Ref: "main", Status: StatusAvailable}).Lookup("gamma")
	assert.ErrorContains(t, err, "no skills are defined on main")

	_, err = (&Catalog{Ref: "main", Status: StatusAbsent}).Lookup("gamma")
	assert.ErrorContains(t, err, "does not exist on main")

	_, err = (&Catalog{Ref: "main", Status: StatusUnavailable, Reason: "listing failed"}).Lookup("gamma")
	assert.ErrorContains(t, err, "skills on main are unavailable: listing failed")

	_, err = available.Lookup("../x")
	assert.ErrorContains(t, err, "invalid skill name")
}

func TestExampleSkillsParse(t *testing.T) {
	const dir = "../../examples/skills"
	entries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Assert(t, len(entries) > 0)
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name(), FileName))
			assert.NilError(t, err)
			skill, err := Parse(entry.Name(), data)
			assert.NilError(t, err)
			assert.Equal(t, skill.Name, entry.Name())
		})
	}
}
