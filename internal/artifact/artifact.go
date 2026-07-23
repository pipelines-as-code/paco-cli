package artifact

import (
	"os"
	"path/filepath"
)

const (
	FileDiff             = ".pr.diff"
	FileValidLines       = ".valid-lines.json"
	FileExistingInline   = ".existing-inline.json"
	FileExistingFeedback = ".existing-feedback.txt"
	FileHeadSHA          = ".head_sha"
	FileError            = ".paco-error"
	FileReview           = ".paco-review.json"
	FileMode             = ".paco-mode"
	FileFailed           = ".paco-failed"
	FileSecurityBlock    = ".paco-security-block"
	FileReviewRules      = ".tekton/ai/REVIEW.md"
)

type Workspace struct {
	Dir string
}

func (w *Workspace) Path(name string) string {
	return filepath.Join(w.Dir, name)
}

func (w *Workspace) Write(name string, data []byte) error {
	path := w.Path(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (w *Workspace) Read(name string) ([]byte, error) {
	return os.ReadFile(w.Path(name))
}

func (w *Workspace) Exists(name string) bool {
	_, err := os.Stat(w.Path(name))
	return err == nil
}

func (w *Workspace) WriteSkip(reason string) error {
	if err := w.Write(FileError, []byte(reason+"\n")); err != nil {
		return err
	}
	if err := w.Write(FileDiff, nil); err != nil {
		return err
	}
	if err := w.Write(FileValidLines, []byte("{}\n")); err != nil {
		return err
	}
	if err := w.Write(FileExistingInline, []byte("{}\n")); err != nil {
		return err
	}
	return w.Write(FileExistingFeedback, nil)
}
