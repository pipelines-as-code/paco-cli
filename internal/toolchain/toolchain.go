// Package toolchain detects the language and runtime versions a repository
// declares in its manifest files, so the reviewer judges code against them
// rather than against the model's training cutoff.
package toolchain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pipelines-as-code/paco-cli/internal/security"
)

// Version is a language or runtime version declared by a repository file.
type Version struct {
	Language string
	Version  string
	Source   string
}

type detector struct {
	file      string
	languages []string
	parse     func([]byte) map[string]string
}

// detectors are checked in order; the first file declaring a language wins.
var detectors = []detector{
	single("go.mod", "Go", matcher(`(?m)^go[ \t]+(\S+)[ \t]*(?://.*)?\r?$`)),
	single("rust-toolchain.toml", "Rust", tomlKey("toolchain", "channel")),
	single("rust-toolchain", "Rust", firstLine),
	single("Cargo.toml", "Rust", tomlKey("package", "rust-version")),
	single(".python-version", "Python", firstLine),
	single("pyproject.toml", "Python", tomlKey("project", "requires-python")),
	single(".nvmrc", "Node.js", firstLine),
	single(".node-version", "Node.js", firstLine),
	single("package.json", "Node.js", packageJSONEngine),
	single(".ruby-version", "Ruby", firstLine),
	single(".java-version", "Java", firstLine),
	{".tool-versions", []string{"Go", "Rust", "Python", "Node.js", "Ruby", "Java"}, toolVersions},
}

// toolVersionsNames maps asdf/mise tool names to language names.
var toolVersionsNames = map[string]string{
	"go":     "Go",
	"golang": "Go",
	"rust":   "Rust",
	"python": "Python",
	"nodejs": "Node.js",
	"node":   "Node.js",
	"ruby":   "Ruby",
	"java":   "Java",
}

var versionRegexp = regexp.MustCompile(`^[0-9A-Za-z<>=~^][0-9A-Za-z._+<>=~^*,|/ -]{0,39}$`)

// Files returns the repository-root file names worth fetching.
func Files() []string {
	files := make([]string, 0, len(detectors))
	for _, d := range detectors {
		files = append(files, d.file)
	}
	return files
}

// Detect parses the given files, keyed by name, and returns one version per
// language following detector priority.
func Detect(files map[string][]byte) []Version {
	seen := map[string]bool{}
	var versions []Version
	for _, d := range detectors {
		data, ok := files[d.file]
		if !ok {
			continue
		}
		parsed := d.parse(data)
		for _, lang := range d.languages {
			v, ok := parsed[lang]
			if !ok || seen[lang] {
				continue
			}
			candidate := Version{Language: lang, Version: strings.TrimSpace(v), Source: d.file}
			if !candidate.valid() {
				continue
			}
			seen[lang] = true
			versions = append(versions, candidate)
		}
	}
	return versions
}

// Format serializes versions for the workspace artifact, one per line.
func Format(versions []Version) []byte {
	var b strings.Builder
	for _, v := range versions {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", v.Language, v.Version, v.Source)
	}
	return []byte(b.String())
}

// Parse reads a workspace artifact written by Format, dropping any line that
// does not come from a known detector or carries an unexpected version.
func Parse(data []byte) []Version {
	var versions []Version
	seen := map[string]bool{}
	for line := range strings.Lines(string(data)) {
		fields := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(fields) != 3 {
			continue
		}
		v := Version{Language: fields[0], Version: fields[1], Source: fields[2]}
		if v.valid() && !seen[v.Language] {
			seen[v.Language] = true
			versions = append(versions, v)
		}
	}
	return versions
}

func (v Version) valid() bool {
	if !versionRegexp.MatchString(v.Version) || security.ScanSecrets(v.Version) != "" {
		return false
	}
	// Ranges may have spaced operators, but free-form words are not versions.
	for _, field := range strings.Fields(v.Version)[1:] {
		if field != "||" && ((field[0] >= 'A' && field[0] <= 'Z') || (field[0] >= 'a' && field[0] <= 'z')) {
			return false
		}
	}
	for _, d := range detectors {
		if d.file != v.Source {
			continue
		}
		for _, lang := range d.languages {
			if lang == v.Language {
				return true
			}
		}
	}
	return false
}

// single builds a detector for a file that declares only one language.
func single(file, lang string, parse func([]byte) string) detector {
	return detector{file, []string{lang}, func(data []byte) map[string]string {
		if v := parse(data); v != "" {
			return map[string]string{lang: v}
		}
		return nil
	}}
}

func matcher(expr string) func([]byte) string {
	re := regexp.MustCompile(expr)
	return func(data []byte) string {
		m := re.FindSubmatch(data)
		if m == nil {
			return ""
		}
		return string(m[1])
	}
}

func tomlKey(table, key string) func([]byte) string {
	re := regexp.MustCompile(`^[ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=[ \t]*["']([^"'\r\n]+)["']`)
	return func(data []byte) string {
		currentTable := ""
		for line := range strings.Lines(string(data)) {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				currentTable = ""
				if name, _, ok := strings.Cut(strings.TrimPrefix(trimmed, "["), "]"); ok {
					currentTable = name
				}
				continue
			}
			if currentTable == table {
				if match := re.FindStringSubmatch(line); match != nil {
					return match[1]
				}
			}
		}
		return ""
	}
}

func firstLine(data []byte) string {
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func packageJSONEngine(data []byte) string {
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Engines.Node
}

func toolVersions(data []byte) map[string]string {
	versions := map[string]string{}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		lang, ok := toolVersionsNames[fields[0]]
		if !ok {
			continue
		}
		if _, dup := versions[lang]; !dup {
			versions[lang] = fields[1]
		}
	}
	return versions
}
