// Package source provides bounded, read-only views of collected PR revisions.
package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/security"
)

const (
	MaxArchiveBytes  = 32 << 20
	MaxSnapshotBytes = 32 << 20
	maxExpandedBytes = 128 << 20
	maxSourceBytes   = 16 << 20
	maxFileBytes     = 512 << 10
	maxFiles         = 10000
	maxEntries       = 100000
)

type Snapshot struct {
	Commit   string            `json:"commit"`
	Files    map[string]string `json:"files"`
	Excluded int               `json:"excluded"`
}

// FromArchive reads regular text files only. Archive paths are never extracted
// onto the filesystem, and symlinks and hardlinks are never followed.
func FromArchive(data []byte, commit string, secrets ...string) (*Snapshot, error) {
	if len(data) > MaxArchiveBytes {
		return nil, errors.New("source archive exceeds 32 MiB")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("opening source archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	limited := &io.LimitedReader{R: gz, N: maxExpandedBytes + 1}
	reader := tar.NewReader(limited)
	c := &collector{s: &Snapshot{Commit: commit, Files: map[string]string{}}, secrets: secrets}
	for entries := 0; ; entries++ {
		header, err := reader.Next()
		if limited.N <= 0 {
			return nil, errors.New("expanded source archive exceeds 128 MiB")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading source archive: %w", err)
		}
		if entries >= maxEntries {
			return nil, errors.New("source archive has too many entries")
		}
		// GitHub tarballs start with a pax_global_header entry holding the
		// commit id; it is metadata, not a file.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if !fs.ValidPath(strings.TrimSuffix(header.Name, "/")) || strings.Contains(header.Name, "\\") {
			return nil, errors.New("source archive contains an invalid path")
		}
		_, name, ok := strings.Cut(header.Name, "/")
		if header.Typeflag == tar.TypeDir || dependencyPath(name) {
			continue
		}
		if !ok || !safePath(name) {
			return nil, errors.New("source archive contains an invalid file path")
		}
		read := func() ([]byte, error) { return io.ReadAll(io.LimitReader(reader, maxFileBytes+1)) }
		if err := c.add(name, header.Typeflag == tar.TypeReg, header.Size, read); err != nil {
			return nil, err
		}
	}
	return c.s, nil
}

// FromDir reads a checked-out working tree with the same filters and limits
// as FromArchive. Symlinks are never followed, and .git and dependency
// directories are not walked.
func FromDir(dir, commit string, secrets ...string) (*Snapshot, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening source directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	c := &collector{s: &Snapshot{Commit: commit, Files: map[string]string{}}, secrets: secrets}
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.EqualFold(entry.Name(), ".git") || dependencyDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if entries++; entries > maxEntries {
			return errors.New("source directory has too many entries")
		}
		if !safePath(name) {
			c.s.Excluded++
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return c.add(name, entry.Type().IsRegular(), info.Size(), func() ([]byte, error) {
			file, err := root.Open(name)
			if err != nil {
				return nil, err
			}
			defer func() { _ = file.Close() }()
			return io.ReadAll(io.LimitReader(file, maxFileBytes+1))
		})
	})
	if err != nil {
		return nil, fmt.Errorf("reading source directory: %w", err)
	}
	return c.s, nil
}

type collector struct {
	s       *Snapshot
	secrets []string
	total   int
}

// add keeps one regular UTF-8 text file. Links and binary files are skipped
// silently because they hold no reviewable source; other skipped files count
// as excluded.
func (c *collector) add(name string, regular bool, size int64, read func() ([]byte, error)) error {
	if dependencyPath(name) || !regular {
		return nil
	}
	if excludedPath(name) || security.ScanSecrets(name, c.secrets...) != "" {
		c.s.Excluded++
		return nil
	}
	content, err := read()
	if err != nil {
		return fmt.Errorf("reading source file: %w", err)
	}
	oversized := size > maxFileBytes || len(content) > maxFileBytes
	if binary(content, oversized) {
		return nil
	}
	if oversized || bytes.Contains(content, []byte("PRIVATE KEY-----")) {
		c.s.Excluded++
		return nil
	}
	if _, exists := c.s.Files[name]; exists {
		return errors.New("source archive contains duplicate paths")
	}
	text := security.Scrub(string(content), c.secrets...)
	if len(text) > maxFileBytes {
		c.s.Excluded++
		return nil
	}
	c.total += len(text)
	if c.total > maxSourceBytes || len(c.s.Files) >= maxFiles {
		return errors.New("source snapshot exceeds 16 MiB or 10000 files")
	}
	c.s.Files[name] = text
	return nil
}

func Decode(data []byte, commit string, secrets ...string) (*Snapshot, error) {
	if len(data) > MaxSnapshotBytes {
		return nil, errors.New("source snapshot exceeds 32 MiB")
	}

	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decoding source snapshot: %w", err)
	}
	if commit == "" || s.Commit != commit {
		return nil, errors.New("source snapshot does not match the reviewed commit")
	}
	if len(s.Files) > maxFiles || s.Excluded < 0 {
		return nil, errors.New("invalid source snapshot metadata")
	}
	total := 0
	for name, content := range s.Files {
		if !safePath(name) || excludedPath(name) || security.ScanSecrets(name, secrets...) != "" || len(content) > maxFileBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) || strings.Contains(content, "PRIVATE KEY-----") {
			return nil, errors.New("source snapshot contains an invalid file")
		}
		content = security.Scrub(content, secrets...)
		if len(content) > maxFileBytes {
			return nil, errors.New("redacted source file exceeds 512 KiB")
		}
		total += len(content)
		if total > maxSourceBytes {
			return nil, errors.New("source snapshot exceeds 16 MiB")
		}
		s.Files[name] = content
	}
	return &s, nil
}

// ValidateCombined applies the original storage limits to both revisions
// together. Optional before context must not double retained source capacity.
func ValidateCombined(head, before *Snapshot) error {
	total, files, encodedTotal := 0, 0, 0
	for _, snapshot := range []*Snapshot{head, before} {
		if snapshot == nil {
			continue
		}
		files += len(snapshot.Files)
		for _, content := range snapshot.Files {
			total += len(content)
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		encodedTotal += len(encoded)
	}
	if total > maxSourceBytes || files > maxFiles {
		return errors.New("combined source snapshots exceed 16 MiB or 10000 files")
	}
	if encodedTotal > MaxSnapshotBytes {
		return errors.New("combined encoded source snapshots exceed 32 MiB")
	}
	return nil
}

// binary reports content with a NUL byte or invalid UTF-8. A read cut at the
// size limit may end inside a character, so up to three trailing bytes of a
// truncated read are ignored.
func binary(content []byte, truncated bool) bool {
	if bytes.IndexByte(content, 0) >= 0 {
		return true
	}
	for i := 1; truncated && i < utf8.UTFMax && !utf8.Valid(content) && len(content) > 0; i++ {
		content = content[:len(content)-1]
	}
	return !utf8.Valid(content)
}

func safePath(name string) bool {
	return name != "." && fs.ValidPath(name) && !strings.ContainsAny(name, "\\\r\n\t\x00")
}

// dependencyDirs are third-party directories that every snapshot leaves out
// on purpose; they are not counted as excluded, since that would mark every
// review of a vendored repository as incomplete.
var dependencyDirs = []string{"vendor", "node_modules", ".venv", "__pycache__"}

// dependencyNote tells the model what a snapshot never contains.
var dependencyNote = "Binary files, symlinks and dependency directories (" + strings.Join(dependencyDirs, ", ") + ") are never included."

func dependencyDir(name string) bool {
	return slices.Contains(dependencyDirs, strings.ToLower(name))
}

func dependencyPath(name string) bool {
	return slices.ContainsFunc(strings.Split(name, "/"), dependencyDir)
}

func excludedPath(name string) bool {
	if dependencyPath(name) {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	base := strings.ToLower(path.Base(name))
	return strings.HasPrefix(base, ".env") || base == "credentials.json" ||
		base == "creds.json" || base == "service-account.json" ||
		strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx")
}
