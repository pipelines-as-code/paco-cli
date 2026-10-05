package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/model"
)

const FileStatus = artifact.FileStatus

// VerificationStatus binds publication to the exact locally verified output.
// This is consistency metadata, not a signature against a hostile workspace.
type VerificationStatus struct {
	Version       int           `json:"version"`
	State         string        `json:"state"`
	HeadSHA       string        `json:"head_sha"`
	BaseRef       string        `json:"base_ref"`
	TargetBaseSHA string        `json:"target_base_sha"`
	Repo          string        `json:"repo"`
	PRNumber      int           `json:"pr_number"`
	ReviewDigest  string        `json:"review_digest"`
	Limitations   []string      `json:"limitations"`
	Candidates    int           `json:"candidates"`
	Rejected      int           `json:"rejected"`
	Duplicates    int           `json:"duplicates"`
	Accepted      int           `json:"accepted"`
	Unanchored    int           `json:"unanchored"`
	Posted        int           `json:"posted"`
	Decisions     []Disposition `json:"decisions"`
	Usage         model.Usage   `json:"usage"`
}

type Disposition struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func WriteStatus(ws *artifact.Workspace, status *VerificationStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return ws.Write(FileStatus, data)
}

func ReadStatus(ws *artifact.Workspace, reviewData []byte) (*VerificationStatus, error) {
	data, err := ws.Read(FileStatus)
	if err != nil {
		return nil, fmt.Errorf("reading verification status: %w", err)
	}
	var status VerificationStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("decoding verification status: %w", err)
	}
	if status.Version != 1 || status.ReviewDigest != Digest(reviewData) ||
		status.HeadSHA == "" || status.BaseRef == "" || status.Repo == "" || status.PRNumber <= 0 {
		return nil, errors.New("verification status does not match review output")
	}
	if status.State != "complete" && status.State != "partial" && status.State != "failed" {
		return nil, errors.New("invalid verification state")
	}
	return &status, nil
}

func clearReviewOutput(ws *artifact.Workspace) error {
	for _, file := range []string{artifact.FileReview, artifact.FileFailed, artifact.FileSecurityBlock, artifact.FileMode, FileStatus} {
		if err := os.Remove(ws.Path(file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clearing review artifact %s: %w", file, err)
		}
	}
	return nil
}
