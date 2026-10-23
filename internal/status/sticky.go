package status

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/artifact"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
)

// Marker starts every Paco review comment.
const Marker = "<!-- paco-review -->"

const (
	bannerStart = "<!-- paco-banner -->"
	bannerEnd   = "<!-- /paco-banner -->"
	// maxCommentChars is GitHub's limit for a comment body.
	maxCommentChars = 65536
)

var headerRE = regexp.MustCompile(`^<!-- paco-status run=([a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?) comment=([0-9]{1,19}|pr) state=([a-z]+) -->$`)

// Header is the metadata line right after Marker. It records which run owns
// the comment; only the comment's fixed second line is ever parsed.
type Header struct {
	Run     string
	Comment string
	State   State
}

func (h Header) String() string {
	return fmt.Sprintf("<!-- paco-status run=%s comment=%s state=%s -->", h.Run, h.Comment, h.State)
}

func parseHeader(body string) (Header, bool) {
	lines := strings.SplitN(body, "\n", 3)
	if len(lines) < 2 || lines[0] != Marker {
		return Header{}, false
	}
	m := headerRE.FindStringSubmatch(lines[1])
	if m == nil {
		return Header{}, false
	}
	s, ok := ParseState(m[3])
	if !ok {
		return Header{}, false
	}
	return Header{Run: m[1], Comment: m[2], State: s}, true
}

// Sticky is Paco's review comment on the pull request.
type Sticky struct {
	ID        int64
	Body      string
	Header    Header
	HasHeader bool
}

// fingerprint lets post tell whether anyone claimed the comment after diff
// looked at it.
func (st *Sticky) fingerprint() string {
	switch {
	case st == nil:
		return "none"
	case !st.HasHeader:
		return "unowned"
	default:
		return st.Header.String()
	}
}

// findSticky only trusts comments written by the bot itself, so a marker
// pasted by someone else is ignored. It prefers the comment owned by runID:
// two runs that start on a PR at once may each create one.
func findSticky(comments []ghclient.IssueComment, login, runID string) *Sticky {
	var first *Sticky
	for _, c := range comments {
		if !strings.EqualFold(c.Login, login) || !strings.HasPrefix(c.Body, Marker) {
			continue
		}
		st := &Sticky{ID: c.ID, Body: c.Body}
		st.Header, st.HasHeader = parseHeader(c.Body)
		if st.owner(runID) {
			return st
		}
		if first == nil {
			first = st
		}
	}
	return first
}

func (st *Sticky) owner(runID string) bool {
	return st != nil && st.HasHeader && st.Header.Run == runID
}

// content strips the marker, header and banner, leaving the review text.
func content(body string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(body, Marker), "\n")
	line, after, _ := strings.Cut(rest, "\n")
	if headerRE.MatchString(line) {
		rest = after
	}
	if strings.HasPrefix(rest, bannerStart) {
		if i := strings.Index(rest, bannerEnd); i >= 0 {
			rest = strings.TrimLeft(rest[i+len(bannerEnd):], "\n")
		}
	}
	return rest
}

func compose(h Header, banner, rest string) string {
	var b strings.Builder
	b.WriteString(Marker + "\n" + h.String() + "\n")
	if banner != "" {
		b.WriteString(bannerStart + "\n" + banner + "\n" + bannerEnd + "\n")
		if rest != "" {
			b.WriteString("\n")
		}
	}
	b.WriteString(rest)
	return b.String()
}

func fits(body string) bool { return utf8.RuneCountInString(body) <= maxCommentChars }

const earlierRun = " The review below is from an earlier run."

func reviewingBanner(hasPrevious bool) string {
	b := "> [!NOTE]\n> ⏳ Paco is reviewing this pull request. This comment will be updated when the review is done."
	if hasPrevious {
		b += earlierRun
	}
	return b
}

func failedBanner(hasPrevious bool) string {
	b := "> [!WARNING]\n> 😕 The latest Paco review failed or was interrupted. Check the PipelineRun logs for details."
	if hasPrevious {
		b += earlierRun
	}
	return b
}

// withBanner adds the banner when it fits; without it when only that fits;
// "" when even the header does not fit, since the review text is never cut.
func withBanner(h Header, banner, rest string) string {
	if body := compose(h, banner, rest); fits(body) {
		return body
	}
	if body := compose(h, "", rest); fits(body) {
		return body
	}
	return ""
}

// Claim records what diff did with the sticky comment for post to check.
type Claim struct {
	RunID   string `json:"run_id"`
	Claimed bool   `json:"claimed"`
	// Prior is the comment's fingerprint before the claim; empty when unknown.
	Prior string `json:"prior,omitempty"`
}

func WriteClaim(ws *artifact.Workspace, c Claim) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return ws.Write(artifact.FileClaim, data)
}

// ReadClaim reports false when diff left no usable claim.
func ReadClaim(ws *artifact.Workspace) (Claim, bool) {
	data, err := ws.Read(artifact.FileClaim)
	if err != nil {
		return Claim{}, false
	}
	var c Claim
	if json.Unmarshal(data, &c) != nil || c.RunID == "" {
		return Claim{}, false
	}
	return c, true
}

// Claim marks the sticky comment as owned by this run with a reviewing
// banner, creating the comment when there is none yet.
func (r *Reporter) Claim(ctx context.Context) Claim {
	c := Claim{RunID: r.runID}
	ctx, cancel := bounded(ctx)
	defer cancel()
	comments, err := r.gh.IssueComments(ctx, r.repo, r.pr)
	if err != nil {
		r.warn("read the review comment", err)
		return c
	}
	st := findSticky(comments, r.login, r.runID)
	c.Prior = st.fingerprint()
	h := r.header(Fetching)
	if st == nil {
		_, err = r.gh.CreateComment(ctx, r.repo, r.pr, compose(h, reviewingBanner(false), ""))
	} else {
		rest := content(st.Body)
		body := withBanner(h, reviewingBanner(rest != ""), rest)
		if body == "" {
			r.log.Line("Warning: the review comment is too large to mark this run; leaving it unchanged")
			return c
		}
		err = r.gh.UpdateComment(ctx, r.repo, st.ID, body)
	}
	if err != nil {
		r.warn("mark the review comment", err)
		return c
	}
	c.Claimed = true
	return c
}

// Resolve tells post whether this run may still publish and which comment to
// update (nil creates one). A run publishes while it owns the comment, even
// if its claim reported an error after GitHub applied it. One that could not
// claim it publishes only if nobody else claimed it since. Read errors are
// returned, as before status reporting existed.
func (r *Reporter) Resolve(ctx context.Context, c Claim) (*Sticky, bool, error) {
	comments, err := r.gh.IssueComments(ctx, r.repo, r.pr)
	if err != nil {
		return nil, false, err
	}
	st := findSticky(comments, r.login, r.runID)
	switch {
	case st == nil, st.owner(r.runID):
		return st, true, nil
	case c.Claimed:
		return st, false, nil
	case c.Prior == "":
		return st, true, nil
	default:
		return st, st.fingerprint() == c.Prior, nil
	}
}

const cutNote = "\n\n_This summary was cut to fit GitHub's comment size limit._"

// Publish writes post's final body, which starts with Marker, under this
// run's header. The header is kept and the summary cut when both do not fit.
func (r *Reporter) Publish(ctx context.Context, st *Sticky, body string, s State) (*Sticky, error) {
	h := r.header(s)
	rest := strings.TrimPrefix(strings.TrimPrefix(body, Marker), "\n")
	full := compose(h, "", rest)
	if !fits(full) {
		keep := maxCommentChars - utf8.RuneCountInString(compose(h, "", "")+cutNote)
		full = compose(h, "", string([]rune(rest)[:keep])+cutNote)
		r.log.Line("Warning: the summary was cut to fit GitHub's comment size limit")
	}
	if st == nil {
		id, err := r.gh.CreateComment(ctx, r.repo, r.pr, full)
		if err != nil {
			return nil, fmt.Errorf("creating the Paco summary comment: %w", err)
		}
		r.log.Line("Publication: summary comment created")
		return &Sticky{ID: id, Body: full, Header: h, HasHeader: true}, nil
	}
	if err := r.gh.UpdateComment(ctx, r.repo, st.ID, full); err != nil {
		return nil, fmt.Errorf("updating the Paco summary comment: %w", err)
	}
	r.log.Line("Publication: summary comment updated")
	return &Sticky{ID: st.ID, Body: full, Header: h, HasHeader: true}, nil
}

// Fail marks the run as failed, but only while it owns the sticky comment, so
// a cancelled run never overwrites a newer one. It does nothing when the
// comment has no header for this run, including when there is no comment.
func (r *Reporter) Fail(ctx context.Context) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	st, ok := r.owned(ctx)
	if !ok {
		return
	}
	if st.Header.State != Failed {
		rest := content(st.Body)
		if body := withBanner(r.header(Failed), failedBanner(rest != ""), rest); body != "" {
			r.warn("mark the review comment as failed", r.gh.UpdateComment(ctx, r.repo, st.ID, body))
		}
	}
	r.Set(ctx, Failed)
}

// Update sets s only while this run owns the review comment, so a slower or
// cancelled older run never changes the status of a newer one.
func (r *Reporter) Update(ctx context.Context, s State) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if _, ok := r.owned(ctx); ok {
		r.Set(ctx, s)
	}
}

// owned returns the review comment when this run owns it.
func (r *Reporter) owned(ctx context.Context) (*Sticky, bool) {
	comments, err := r.gh.IssueComments(ctx, r.repo, r.pr)
	if err != nil {
		r.warn("read the review comment", err)
		return nil, false
	}
	st := findSticky(comments, r.login, r.runID)
	if !st.owner(r.runID) {
		r.log.Line("Status: this run does not own the review comment; leaving the pull request unchanged")
		return nil, false
	}
	return st, true
}
