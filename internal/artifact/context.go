package artifact

const (
	FileInputManifest        = ".paco-input.json"
	FileSourceBefore         = ".paco-source-before.json"
	FileExistingFeedbackJSON = ".existing-feedback.json"
)

// InputManifest identifies the exact redacted inputs used for review. Target
// base supplies trusted rules; merge base supplies the old side of the PR diff.
type InputManifest struct {
	Version       int          `json:"version"`
	Repo          string       `json:"repo"`
	PRNumber      int          `json:"pr_number"`
	HeadSHA       string       `json:"head_sha"`
	TargetBaseSHA string       `json:"target_base_sha"`
	MergeBaseSHA  string       `json:"merge_base_sha"`
	DiffDigest    string       `json:"diff_digest"`
	ContextStatus string       `json:"context_status"`
	Head          ContextState `json:"head"`
	Before        ContextState `json:"before"`
	Limitations   []string     `json:"limitations,omitempty"`
}

type ContextState struct {
	Status   string `json:"status"`
	Excluded int    `json:"excluded"`
	Reason   string `json:"reason,omitempty"`
}

type TrustedFeedback struct {
	Version   int              `json:"version"`
	Status    string           `json:"status"`
	Truncated bool             `json:"truncated"`
	Comments  []TrustedComment `json:"comments"`
}

type TrustedComment struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated,omitempty"`
}
