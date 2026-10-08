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
		if header.Typeflag == tar.TypeDir {
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
// as FromArchive. Symlinks are never followed and .git directories are skipped.
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
			if strings.EqualFold(entry.Name(), ".git") {
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

// add keeps one regular UTF-8 text file, counting skipped files as excluded.
func (c *collector) add(name string, regular bool, size int64, read func() ([]byte, error)) error {
	if !regular || excludedPath(name) || size > maxFileBytes || security.ScanSecrets(name, c.secrets...) != "" {
		c.s.Excluded++
		return nil
	}
	content, err := read()
	if err != nil {
		return fmt.Errorf("reading source file: %w", err)
	}
	if len(content) > maxFileBytes || !utf8.Valid(content) || bytes.ContainsRune(content, 0) || bytes.Contains(content, []byte("PRIVATE KEY-----")) {
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

func safePath(name string) bool {
	return name != "." && fs.ValidPath(name) && !strings.ContainsAny(name, "\\\r\n\t\x00")
}

func excludedPath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		switch strings.ToLower(part) {
		case ".git", "node_modules", "vendor", ".venv", "__pycache__":
			return true
		}
	}
	base := strings.ToLower(path.Base(name))
	return strings.HasPrefix(base, ".env") || base == "credentials.json" ||
		base == "creds.json" || base == "service-account.json" ||
		strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx")
}
