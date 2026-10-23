// Package status shows a Paco run's progress on the pull request: a reaction
// on the trigger, a status label, and a header and banner in the sticky
// review comment. Reporting is best effort and never fails a review.
package status

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
)

type State string

const (
	Fetching   State = "fetching"
	Reviewing  State = "reviewing"
	Clean      State = "clean"
	Findings   State = "findings"
	Summarized State = "summarized"
	Failed     State = "failed"
)

// DefaultPrefix keeps status labels next to the paco/review-* difficulty labels.
const DefaultPrefix = "paco/"

const reportTimeout = 15 * time.Second

type look struct {
	reaction string
	// label is the suffix after the prefix; empty removes every status label.
	label string
	color string
	desc  string
}

var looks = map[State]look{
	Fetching:   {"eyes", "reviewing", "1d76db", "Paco is reviewing this pull request"},
	Reviewing:  {"rocket", "reviewing", "1d76db", "Paco is reviewing this pull request"},
	Clean:      {"+1", "clean", "0e8a16", "Paco review found nothing to report"},
	Findings:   {"-1", "findings", "d93f0b", "Paco review posted findings"},
	Summarized: {"hooray", "", "", ""},
	Failed:     {"confused", "failed", "b60205", "Paco review failed or was interrupted"},
}

var labelSuffixes = []string{"reviewing", "clean", "findings", "failed"}

func isStatusReaction(content string) bool {
	for _, l := range looks {
		if l.reaction == content {
			return true
		}
	}
	return false
}

// ParseState accepts only the known states.
func ParseState(s string) (State, bool) {
	_, ok := looks[State(s)]
	return State(s), ok
}

var runIDRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// ValidRunID accepts Kubernetes object names, such as a PipelineRun name.
func ValidRunID(s string) bool { return runIDRE.MatchString(s) }

// NewRunID identifies a run started outside Tekton.
func NewRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "local-" + hex.EncodeToString(b)
}

type Config struct {
	GitHub *ghclient.Client
	Repo   ghclient.Repo
	PR     int
	// CommentID is the trigger comment; empty or invalid reacts on the PR.
	CommentID string
	RunID     string
	Labels    bool
	Prefix    string
	// Log defaults to stdout with the GitHub token scrubbed.
	Log *progress.Logger
}

type Reporter struct {
	gh        *ghclient.Client
	repo      ghclient.Repo
	pr        int
	commentID int64
	runID     string
	labels    bool
	prefix    string
	login     string
	log       *progress.Logger
}

// New resolves the bot login with a read-only query. It returns nil, after
// logging why, when status reporting is unavailable for this run.
func New(ctx context.Context, cfg Config) *Reporter {
	log := cfg.Log
	if log == nil {
		log = progress.New(nil, []string{cfg.GitHub.Token()})
	}
	if !ValidRunID(cfg.RunID) {
		log.Line("Warning: invalid run ID %q; status reporting disabled", cfg.RunID)
		return nil
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	login, err := cfg.GitHub.Viewer(ctx)
	if err != nil {
		log.Line("Warning: could not identify the GitHub App user; status reporting disabled: %s", err.Error())
		return nil
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	id, _ := ghclient.ParseCommentID(cfg.CommentID)
	return &Reporter{
		gh: cfg.GitHub, repo: cfg.Repo, pr: cfg.PR, commentID: id, runID: cfg.RunID,
		labels: cfg.Labels, prefix: prefix, login: login, log: log,
	}
}

// bounded keeps reporting short and lets it run after the caller's context
// was cancelled, so a failing step can still report its failure.
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
}

func (r *Reporter) warn(action string, err error) {
	if err != nil {
		r.log.Line("Warning: could not %s: %s", action, err.Error())
	}
}

func (r *Reporter) header(s State) Header {
	comment := "pr"
	if r.commentID != 0 {
		comment = strconv.FormatInt(r.commentID, 10)
	}
	return Header{Run: r.runID, Comment: comment, State: s}
}

// Set swaps the trigger reaction and the status label to s.
func (r *Reporter) Set(ctx context.Context, s State) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	r.react(ctx, s)
	if r.labels {
		r.label(ctx, s)
	}
	r.log.Line("Status: %s", string(s))
}

// react adds the new reaction before removing the bot's previous status
// reactions, so the trigger never shows none.
func (r *Reporter) react(ctx context.Context, s State) {
	want := looks[s].reaction
	var err error
	if r.commentID != 0 {
		_, err = r.gh.AddCommentReaction(ctx, r.repo, r.commentID, want)
	} else {
		_, err = r.gh.AddIssueReaction(ctx, r.repo, r.pr, want)
	}
	if err != nil {
		r.warn("set the "+want+" reaction", err)
		return
	}
	r.prune(ctx, r.commentID, want)
}

// prune removes the bot's status reactions other than keep from a comment,
// or from the pull request when commentID is 0. Other users' reactions and
// unrelated contents are left alone.
func (r *Reporter) prune(ctx context.Context, commentID int64, keep string) {
	var current []ghclient.Reaction
	var err error
	if commentID != 0 {
		current, err = r.gh.CommentReactions(ctx, r.repo, commentID)
	} else {
		current, err = r.gh.IssueReactions(ctx, r.repo, r.pr)
	}
	if err != nil {
		r.warn("list reactions", err)
		return
	}
	for _, rc := range current {
		if !strings.EqualFold(rc.Login, r.login) || rc.Content == keep || !isStatusReaction(rc.Content) {
			continue
		}
		if commentID != 0 {
			err = r.gh.DeleteCommentReaction(ctx, r.repo, commentID, rc.ID)
		} else {
			err = r.gh.DeleteIssueReaction(ctx, r.repo, r.pr, rc.ID)
		}
		r.warn("remove the "+rc.Content+" reaction", err)
	}
}

func (r *Reporter) label(ctx context.Context, s State) {
	present, err := r.gh.IssueLabels(ctx, r.repo, r.pr)
	if err != nil {
		r.warn("list labels", err)
		return
	}
	has := map[string]bool{}
	for _, name := range present {
		has[strings.ToLower(name)] = true
	}
	want := ""
	if l := looks[s]; l.label != "" {
		want = r.prefix + l.label
		if !has[strings.ToLower(want)] {
			if err := r.gh.EnsureLabel(ctx, r.repo, want, l.color, l.desc); err != nil {
				r.warn("create label "+want, err)
				return
			}
			if err := r.gh.AddLabel(ctx, r.repo, r.pr, want); err != nil {
				r.warn("add label "+want, err)
				return
			}
		}
	}
	for _, suffix := range labelSuffixes {
		name := r.prefix + suffix
		if name != want && has[strings.ToLower(name)] {
			r.warn("remove label "+name, r.gh.RemoveLabel(ctx, r.repo, r.pr, name))
		}
	}
}
