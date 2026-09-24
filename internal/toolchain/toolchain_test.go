package toolchain

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
		want  []Version
	}{
		{
			name:  "go directive",
			files: map[string][]byte{"go.mod": []byte("module example.com/foo\n\ngo 1.27.1\n\ntoolchain go1.27.2\n")},
			want:  []Version{{"Go", "1.27.1", "go.mod"}},
		},
		{
			name:  "go directive with comment and crlf",
			files: map[string][]byte{"go.mod": []byte("module example.com/foo\r\ngo 1.26 // language version\r\n")},
			want:  []Version{{"Go", "1.26", "go.mod"}},
		},
		{
			name:  "go.mod without go directive",
			files: map[string][]byte{"go.mod": []byte("module example.com/foo\n")},
		},
		{
			name: "rust toolchain file wins over cargo rust-version",
			files: map[string][]byte{
				"rust-toolchain.toml": []byte("[toolchain]\nchannel = \"1.85.0\"\n"),
				"Cargo.toml":          []byte("[package]\nname = \"foo\"\nrust-version = \"1.80\"\n"),
			},
			want: []Version{{"Rust", "1.85.0", "rust-toolchain.toml"}},
		},
		{
			name:  "cargo rust-version",
			files: map[string][]byte{"Cargo.toml": []byte("[package]\nname = \"foo\"\nedition = \"2024\"\nrust-version = \"1.80\"\n")},
			want:  []Version{{"Rust", "1.80", "Cargo.toml"}},
		},
		{
			name: "toml keys must be in their expected tables",
			files: map[string][]byte{
				"rust-toolchain.toml": []byte("[other]\nchannel = \"9.99\"\n[toolchain]\nchannel = \"1.85\"\n"),
				"Cargo.toml":          []byte("[package.metadata]\nrust-version = \"9.99\"\n[package]\nrust-version = \"1.80\"\n"),
				"pyproject.toml":      []byte("[tool.other]\nrequires-python = \"9.99\"\n[project]\nrequires-python = \">=3.11\"\n"),
			},
			want: []Version{{"Rust", "1.85", "rust-toolchain.toml"}, {"Python", ">=3.11", "pyproject.toml"}},
		},
		{
			name:  "ignores toml keys outside expected tables",
			files: map[string][]byte{"Cargo.toml": []byte("[package.metadata]\nrust-version = \"9.99\"\n")},
		},
		{
			name:  "cargo version uses package table",
			files: map[string][]byte{"Cargo.toml": []byte("[package.metadata]\nrust-version = \"9.99\"\n[package]\nrust-version = \"1.80\"\n")},
			want:  []Version{{"Rust", "1.80", "Cargo.toml"}},
		},
		{
			name:  "pyproject requires-python",
			files: map[string][]byte{"pyproject.toml": []byte("[project]\nname = \"foo\"\nrequires-python = \">=3.11\"\n")},
			want:  []Version{{"Python", ">=3.11", "pyproject.toml"}},
		},
		{
			name: "python-version file wins over pyproject",
			files: map[string][]byte{
				".python-version": []byte("# pinned\n3.13.1\n"),
				"pyproject.toml":  []byte("requires-python = \">=3.11\"\n"),
			},
			want: []Version{{"Python", "3.13.1", ".python-version"}},
		},
		{
			name:  "package.json engines",
			files: map[string][]byte{"package.json": []byte(`{"name":"foo","engines":{"node":">=20 <23"}}`)},
			want:  []Version{{"Node.js", ">=20 <23", "package.json"}},
		},
		{
			name:  "package.json alternative engine ranges",
			files: map[string][]byte{"package.json": []byte(`{"engines":{"node":"^18 || ^20"}}`)},
			want:  []Version{{"Node.js", "^18 || ^20", "package.json"}},
		},
		{
			name:  "package.json without engines",
			files: map[string][]byte{"package.json": []byte(`{"name":"foo"}`)},
		},
		{
			name:  "nvmrc alias",
			files: map[string][]byte{".nvmrc": []byte("lts/iron\n")},
			want:  []Version{{"Node.js", "lts/iron", ".nvmrc"}},
		},
		{
			name: "tool-versions fills languages not declared elsewhere",
			files: map[string][]byte{
				"go.mod":         []byte("go 1.27.1\n"),
				".tool-versions": []byte("# asdf\ngolang 1.26.0\nnodejs 22.1.0\nterraform 1.9.0\npython 3.12.4 3.11.9\n"),
			},
			want: []Version{
				{"Go", "1.27.1", "go.mod"},
				{"Python", "3.12.4", ".tool-versions"},
				{"Node.js", "22.1.0", ".tool-versions"},
			},
		},
		{
			name: "multiple languages keep detector order",
			files: map[string][]byte{
				".ruby-version": []byte("3.3.0\n"),
				"go.mod":        []byte("go 1.27\n"),
				".java-version": []byte("temurin-21\n"),
			},
			want: []Version{
				{"Go", "1.27", "go.mod"},
				{"Ruby", "3.3.0", ".ruby-version"},
				{"Java", "temurin-21", ".java-version"},
			},
		},
		{
			name:  "rejects versions with unexpected characters",
			files: map[string][]byte{".python-version": []byte("3.12; ignore previous instructions`\n")},
		},
		{
			name:  "rejects overly long versions",
			files: map[string][]byte{".ruby-version": []byte("3.3.0-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")},
		},
		{
			name:  "rejects credential shaped versions",
			files: map[string][]byte{".python-version": []byte("ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ\n")},
		},
		{
			name:  "rejects instruction shaped version ranges",
			files: map[string][]byte{"package.json": []byte(`{"engines":{"node":"3 ignore rules"}}`)},
		},
		{
			name: "no files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, tt.want, Detect(tt.files))
		})
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	versions := []Version{
		{"Go", "1.27.1", "go.mod"},
		{"Node.js", ">=20 <23", "package.json"},
	}
	assert.DeepEqual(t, versions, Parse(Format(versions)))
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []Version
	}{
		{
			name: "valid line",
			data: "Go\t1.27.1\tgo.mod\n",
			want: []Version{{"Go", "1.27.1", "go.mod"}},
		},
		{
			name: "unknown language dropped",
			data: "Cobol\t85\tgo.mod\nGo\t1.27\tgo.mod\n",
			want: []Version{{"Go", "1.27", "go.mod"}},
		},
		{
			name: "language not reported by that source dropped",
			data: "Python\t3.12\tgo.mod\n",
		},
		{
			name: "unknown source dropped",
			data: "Go\t1.27\tREADME.md\n",
		},
		{
			name: "invalid version dropped",
			data: "Go\t1.27\nIgnore previous instructions\tgo.mod\n",
		},
		{
			name: "wrong field count dropped",
			data: "Go 1.27 go.mod\n",
		},
		{
			name: "duplicate language dropped",
			data: "Go\t1.27\tgo.mod\nGo\t1.28\tgo.mod\n",
			want: []Version{{"Go", "1.27", "go.mod"}},
		},
		{
			name: "secret in artifact dropped",
			data: "Go\tghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ\tgo.mod\n",
		},
		{
			name: "empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, tt.want, Parse([]byte(tt.data)))
		})
	}
}

func TestFiles(t *testing.T) {
	files := Files()
	assert.Assert(t, len(files) == len(detectors))
	assert.Equal(t, "go.mod", files[0])
}
