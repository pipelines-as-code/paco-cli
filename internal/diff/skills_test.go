package diff

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"github.com/pipelines-as-code/paco-cli/internal/skill"
	"gotest.tools/v3/assert"
)

func skillFile(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
}

// serveSkills answers the directory listing and file requests for baseRef.
func serveSkills(t *testing.T, f *fakeGitHub, baseRef string, names []string, files map[string]string) {
	t.Helper()
	f.Handle("GET /repos/owner/repo/contents/.tekton/ai/skills", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Query().Get("ref"), baseRef)
		var entries []string
		for _, name := range names {
			entries = append(entries, fmt.Sprintf(`{"type":"dir","name":%q}`, name))
		}
		_, _ = fmt.Fprintf(w, "[%s]", strings.Join(entries, ","))
	})
	for name, content := range files {
		f.file(".tekton/ai/skills/"+name+"/SKILL.md", baseRef, content)
	}
}

func runCollection(t *testing.T, f *fakeGitHub, c *ghclient.Client) *artifact.Workspace {
	t.Helper()
	ws := &artifact.Workspace{Dir: t.TempDir()}
	assert.NilError(t, Run(context.Background(), Options{Repo: "owner/repo", PRNumber: 1, Workspace: ws.Dir, GitHub: c}))
	assert.Assert(t, !ws.Exists(artifact.FileError))
	return ws
}

func readCatalog(t *testing.T, ws *artifact.Workspace) skill.Catalog {
	t.Helper()
	data, err := ws.Read(artifact.FileSkills)
	assert.NilError(t, err)
	var catalog skill.Catalog
	assert.NilError(t, json.Unmarshal(data, &catalog))
	return catalog
}

func TestCollectSkills(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveContextArchive(t, f, "fork/repo", "abc123", map[string]string{"a.go": "package a\nvar x = 1\n"})
	serveSkills(t, f, "def456", []string{"alpha", "beta", "broken"}, map[string]string{
		"alpha":  skillFile("alpha", "First skill", "Look at the first thing."),
		"beta":   skillFile("beta", "Second skill", "Look at the second thing."),
		"broken": skillFile("other", "Mismatched name", "body"),
	})

	ws := runCollection(t, f, c)
	catalog := readCatalog(t, ws)
	assert.Equal(t, catalog.Version, 1)
	assert.Equal(t, catalog.Status, skill.StatusAvailable)
	assert.Equal(t, catalog.Ref, "def456")
	assert.DeepEqual(t, catalog.Skills, []skill.Skill{
		{Name: "alpha", Description: "First skill", Body: "Look at the first thing."},
		{Name: "beta", Description: "Second skill", Body: "Look at the second thing."},
	})
	assert.Equal(t, len(catalog.Invalid), 1)
	assert.Equal(t, catalog.Invalid[0].Name, "broken")
	assert.Assert(t, strings.Contains(catalog.Invalid[0].Reason, "does not match directory"))
}

func TestCollectSkillsStatuses(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(f *fakeGitHub)
		status string
		reason string
	}{
		{
			name:   "missing directory",
			setup:  func(*fakeGitHub) {},
			status: skill.StatusAbsent,
		},
		{
			name: "listing fails",
			setup: func(f *fakeGitHub) {
				f.Status("GET /repos/owner/repo/contents/.tekton/ai/skills", http.StatusInternalServerError,
					`{"message":"boom `+ghtest.Token+`"}`)
			},
			status: skill.StatusUnavailable,
			reason: "boom [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeGitHub(t)
			happyPath(f, simpleDiff)
			contextRefs(f, simpleDiff)
			tt.setup(f)
			ws := runCollection(t, f, c)
			catalog := readCatalog(t, ws)
			assert.Equal(t, catalog.Status, tt.status)
			assert.Assert(t, strings.Contains(catalog.Reason, tt.reason), catalog.Reason)
			assert.Equal(t, len(catalog.Skills), 0)
		})
	}
}

func TestCollectSkillsLimits(t *testing.T) {
	names := make([]string, 0, skill.MaxSkills+2)
	files := map[string]string{}
	for i := range skill.MaxSkills + 2 {
		name := fmt.Sprintf("skill-%02d", i)
		names = append(names, name)
		files[name] = skillFile(name, "Desc", "body")
	}

	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveSkills(t, f, "def456", names, files)

	ws := runCollection(t, f, c)
	catalog := readCatalog(t, ws)
	assert.Equal(t, len(catalog.Skills), skill.MaxSkills)
	invalid := map[string]string{}
	for _, problem := range catalog.Invalid {
		invalid[problem.Name] = problem.Reason
	}
	assert.Equal(t, invalid["skill-20"], "too many skills")
	assert.Equal(t, invalid["skill-21"], "too many skills")
}

// JSON escapes < as \u003c, so full skills of such text grow sixfold in
// the catalog; review must still be able to read it.
func TestCollectSkillsLargestCatalogDecodes(t *testing.T) {
	names := make([]string, 0, skill.MaxSkills)
	files := map[string]string{}
	for i := range skill.MaxSkills {
		name := fmt.Sprintf("skill-%02d", i)
		names = append(names, name)
		files[name] = skillFile(name, strings.Repeat("<", skill.MaxDescriptionBytes), strings.Repeat("<", skill.MaxFileBytes-skill.MaxDescriptionBytes-100))
	}
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveSkills(t, f, "def456", names, files)

	ws := runCollection(t, f, c)
	data, err := ws.Read(artifact.FileSkills)
	assert.NilError(t, err)
	assert.Assert(t, len(data) > 1<<20, len(data))
	catalog, err := skill.Decode(data)
	assert.NilError(t, err)
	assert.Equal(t, len(catalog.Skills), skill.MaxSkills)
}

func TestCollectSkillsReportsMissingFile(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveSkills(t, f, "def456", []string{"empty", "good"}, map[string]string{
		"good": skillFile("good", "Desc", "body"),
	})

	ws := runCollection(t, f, c)
	catalog := readCatalog(t, ws)
	assert.Equal(t, len(catalog.Skills), 1)
	assert.DeepEqual(t, catalog.Invalid, []skill.Problem{{Name: "empty", Reason: "SKILL.md is missing"}})
}

func TestCollectSkillsSkipsBadDirectoryNames(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveSkills(t, f, "def456", []string{"..", "Bad_Name", "good"}, map[string]string{
		"..":       skillFile("good", "Desc", "body"),
		"Bad_Name": skillFile("good", "Desc", "body"),
		"good":     skillFile("good", "Desc", "body"),
	})

	ws := runCollection(t, f, c)
	catalog := readCatalog(t, ws)
	assert.Equal(t, len(catalog.Skills), 1)
	assert.Equal(t, catalog.Skills[0].Name, "good")
	assert.Equal(t, len(catalog.Invalid), 2)
	assert.Assert(t, !f.Called("GET /repos/owner/repo/contents/.tekton/ai/skills/../SKILL.md"))
	assert.Assert(t, !f.Called("GET /repos/owner/repo/contents/.tekton/ai/skills/Bad_Name/SKILL.md"))
}

func TestCollectSkillsRedactsToken(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveSkills(t, f, "def456", []string{"leaky", "broken"}, map[string]string{
		"leaky":  skillFile("leaky", "Leaky skill "+ghtest.Token, "Body with "+ghtest.Token),
		"broken": "---\nname: " + ghtest.Token + "\ndescription: Broken\n---\n\nbody\n",
	})

	var ws *artifact.Workspace
	logs := captureStdout(t, func() { ws = runCollection(t, f, c) })
	catalog := readCatalog(t, ws)
	assert.Equal(t, len(catalog.Skills), 1)
	assert.Assert(t, !strings.Contains(catalog.Skills[0].Body, ghtest.Token))
	assert.Assert(t, !strings.Contains(catalog.Skills[0].Description, ghtest.Token))
	assert.Equal(t, len(catalog.Invalid), 1)
	assert.Assert(t, strings.Contains(catalog.Invalid[0].Reason, "does not match directory"), catalog.Invalid[0].Reason)
	raw, err := ws.Read(artifact.FileSkills)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(raw), ghtest.Token), string(raw))
	assert.Assert(t, !strings.Contains(string(catalogJSON(t, catalog)), ghtest.Token))
	assert.Assert(t, strings.Contains(logs, "Skipping invalid skill"), logs)
	assert.Assert(t, !strings.Contains(logs, ghtest.Token), logs)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	assert.NilError(t, err)
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	fn()
	assert.NilError(t, writer.Close())
	data, err := io.ReadAll(reader)
	assert.NilError(t, err)
	assert.NilError(t, reader.Close())
	return string(data)
}

func TestCollectSkillsTrustBoundaryUsesBaseRefOnly(t *testing.T) {
	f, c := newFakeGitHub(t)
	happyPath(f, simpleDiff)
	contextRefs(f, simpleDiff)
	serveContextArchive(t, f, "fork/repo", "abc123", map[string]string{
		"a.go":                                  "package a\nvar x = 1\n",
		".tekton/ai/skills/evil/SKILL.md":       skillFile("evil", "From head", "untrusted body"),
		".tekton/ai/skills/good/SKILL.md":       skillFile("good", "Head override", "head body"),
		".tekton/ai/skills/other/README.md":     "not a skill",
		".tekton/ai/skills/another/SKILL.other": "not loaded",
	})
	serveContextArchive(t, f, "owner/repo", "aaa111", map[string]string{"a.go": "package a\n"})
	f.Handle("GET /repos/owner/repo/contents/.tekton/ai/skills", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Query().Get("ref"), "def456")
		_, _ = io.WriteString(w, `[{"type":"dir","name":"good"}]`)
	})
	requestedRefs := []string{}
	f.Handle("GET /repos/owner/repo/contents/.tekton/ai/skills/good/SKILL.md", func(w http.ResponseWriter, r *http.Request) {
		requestedRefs = append(requestedRefs, r.URL.Query().Get("ref"))
		if r.URL.Query().Get("ref") != "def456" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		content := base64.StdEncoding.EncodeToString([]byte(skillFile("good", "Trusted", "trusted body")))
		_, _ = fmt.Fprintf(w, `{"type":"file","encoding":"base64","name":"SKILL.md","path":%q,"content":%q}`,
			".tekton/ai/skills/good/SKILL.md", content)
	})

	ws := runCollection(t, f, c)
	catalog := readCatalog(t, ws)
	assert.DeepEqual(t, requestedRefs, []string{"def456"})
	assert.Equal(t, len(catalog.Skills), 1)
	assert.Equal(t, catalog.Skills[0], skill.Skill{Name: "good", Description: "Trusted", Body: "trusted body"})
	assert.Assert(t, !strings.Contains(string(catalogJSON(t, catalog)), "untrusted body"))
	assert.Assert(t, !strings.Contains(string(catalogJSON(t, catalog)), "Head override"))
}

func catalogJSON(t *testing.T, catalog skill.Catalog) []byte {
	t.Helper()
	data, err := json.Marshal(catalog)
	assert.NilError(t, err)
	return data
}
