package source

// Diff contains the redacted comparison text with explicit old/new coordinates.
type Diff struct {
	Files []FileDiff `json:"files"`
}

type FileDiff struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Status  string `json:"status"`
	OldMode string `json:"old_mode,omitempty"`
	NewMode string `json:"new_mode,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
	Hunks   []Hunk `json:"hunks"`
}

type Hunk struct {
	OldStart int        `json:"old_start"`
	OldCount int        `json:"old_count"`
	NewStart int        `json:"new_start"`
	NewCount int        `json:"new_count"`
	Complete bool       `json:"complete"`
	Lines    []DiffLine `json:"lines"`
}

type DiffLine struct {
	Kind      string `json:"kind"`
	OldLine   int    `json:"old_line,omitempty"`
	NewLine   int    `json:"new_line,omitempty"`
	Content   string `json:"content"`
	NoNewline bool   `json:"no_newline,omitempty"`
}
