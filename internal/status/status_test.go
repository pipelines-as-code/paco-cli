package status

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pipelines-as-code/paco-cli/internal/ghclient"
	"github.com/pipelines-as-code/paco-cli/internal/ghclient/ghtest"
	"github.com/pipelines-as-code/paco-cli/internal/progress"
	"gotest.tools/v3/assert"
)

const bot = "paco[bot]"

var repo = ghclient.Repo{Owner: "owner", Name: "repo"}

func setup(t *testing.T) (*ghtest.Fake, *ghtest.PR, *ghclient.Client) {
	t.Helper()
	f, c := ghtest.New(t)
	return f, f.PR(bot), c
}

func reporter(t *testing.T, c *ghclient.Client, runID, commentID string) (*Reporter, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	r := New(context.Background(), Config{
		GitHub: c, Repo: repo, PR: 1, CommentID: commentID, RunID: runID,
		Labels: true, Log: progress.New(&log, nil),
	})
	assert.Assert(t, r != nil, log.String())
	return r, &log
}

func TestSetSwapsOnlyTheBotsStatusReactionsAndLabels(t *testing.T) {
	_, p, c := setup(t)
	p.AddReaction("55", bot, "eyes")
	p.AddReaction("55", bot, "heart")
	p.AddReaction("55", "alice", "rocket")
	p.Labels = []string{"bug", "paco/reviewing", "paco/review-easy"}
	r, _ := reporter(t, c, "run-a", "55")

	r.Set(context.Background(), Clean)
	assert.DeepEqual(t, p.ReactionsBy("55", bot), []string{"heart", "+1"})
	assert.DeepEqual(t, p.ReactionsBy("55", "alice"), []string{"rocket"})
	assert.DeepEqual(t, p.Labels, []string{"bug", "paco/review-easy", "paco/clean"})

	r.Set(context.Background(), Summarized)
	assert.DeepEqual(t, p.ReactionsBy("55", bot), []string{"heart", "hooray"})
	assert.DeepEqual(t, p.Labels, []string{"bug", "paco/review-easy"})
}

func TestSetWithoutLabelsReactsOnThePR(t *testing.T) {
	f, p, c := setup(t)
	r := New(context.Background(), Config{GitHub: c, Repo: repo, PR: 1, RunID: "run-a", Log: progress.New(&bytes.Buffer{}, nil)})

	r.Set(context.Background(), Failed)
	assert.DeepEqual(t, p.ReactionsBy("pr", bot), []string{"confused"})
	for _, call := range f.Calls("") {
		assert.Assert(t, !strings.Contains(call.Key, "labels"), call.Key)
	}
}

func TestSetErrorsAreWarnings(t *testing.T) {
	_, p, c := setup(t)
	p.Fail["POST /repos/owner/repo/issues/1/reactions"] = 500
	p.Fail["GET /repos/owner/repo/issues/1/labels"] = 500
	r, log := reporter(t, c, "run-a", "")

	r.Set(context.Background(), Fetching)
	assert.Assert(t, strings.Contains(log.String(), "could not set the eyes reaction"), log.String())
	assert.Assert(t, strings.Contains(log.String(), "could not list labels"), log.String())
}

func TestNewWithoutIdentityOrRunIDMakesNoRequests(t *testing.T) {
	f, c := ghtest.New(t)
	f.PR("")
	var log bytes.Buffer
	assert.Assert(t, New(context.Background(), Config{GitHub: c, Repo: repo, PR: 1, RunID: "run-a", Log: progress.New(&log, nil)}) == nil)
	assert.Assert(t, strings.Contains(log.String(), "status reporting disabled"))
	assert.Assert(t, New(context.Background(), Config{GitHub: c, Repo: repo, PR: 1, RunID: "Bad_ID", Log: progress.New(&log, nil)}) == nil)
	assert.Equal(t, len(f.Calls("")), 1, "only the viewer query")
	assert.Equal(t, len(f.Writes()), 0)
}

func TestClaimIgnoresSpoofedComments(t *testing.T) {
	_, p, c := setup(t)
	// Someone else pastes a marker and a header claiming another run.
	spoof := p.AddComment("mallory", Marker+"\n"+Header{Run: "run-b", Comment: "pr", State: Clean}.String()+"\nlooks good")
	r, _ := reporter(t, c, "run-a", "")

	claim := r.Claim(context.Background())
	assert.DeepEqual(t, claim, Claim{RunID: "run-a", Claimed: true, Prior: "none"})
	assert.Equal(t, len(p.Comments), 2)
	assert.Assert(t, strings.Contains(p.Body(spoof), "looks good"))
	created := p.Comments[1]
	assert.Equal(t, created.Login, bot)
	h, ok := parseHeader(created.Body)
	assert.Assert(t, ok)
	assert.Equal(t, h, Header{Run: "run-a", Comment: "pr", State: Fetching})
}

func TestHeaderIsOnlyReadFromTheSecondLine(t *testing.T) {
	inBody := Marker + "\n## Paco Review\n" + Header{Run: "run-b", Comment: "pr", State: Clean}.String()
	_, ok := parseHeader(inBody)
	assert.Assert(t, !ok)
	_, ok = parseHeader(Marker + "\n<!-- paco-status run=run-b comment=pr state=bogus -->\n")
	assert.Assert(t, !ok)
	_, ok = parseHeader(Marker + "\n<!-- paco-status run=run-b comment=pr state=clean --> extra\n")
	assert.Assert(t, !ok)
}

func TestBannerTransitionsKeepThePreviousReview(t *testing.T) {
	_, p, c := setup(t)
	id := p.AddComment(bot, Marker+"\n## Paco Review\n\nprevious review")
	r, _ := reporter(t, c, "run-a", "")

	claim := r.Claim(context.Background())
	assert.DeepEqual(t, claim, Claim{RunID: "run-a", Claimed: true, Prior: "unowned"})
	r.Claim(context.Background())
	r.Fail(context.Background())
	r.Fail(context.Background())

	body := p.Body(id)
	h, _ := parseHeader(body)
	assert.Equal(t, h.State, Failed)
	assert.Equal(t, strings.Count(body, bannerStart), 1, body)
	assert.Assert(t, strings.Contains(body, "failed or was interrupted"), body)
	assert.Assert(t, strings.Contains(body, "earlier run"), body)
	assert.Equal(t, content(body), "## Paco Review\n\nprevious review")
	assert.DeepEqual(t, p.ReactionsBy("pr", bot), []string{"confused"})
}

func TestClaimNearTheSizeLimit(t *testing.T) {
	headerOnly := maxCommentChars - len(Marker) - len(Header{Run: "run-a", Comment: "pr", State: Fetching}.String()) - 2
	tests := []struct {
		name    string
		restLen int
		claimed bool
	}{
		{"banner dropped", headerOnly, true},
		{"too large to mark", headerOnly + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, p, c := setup(t)
			previous := Marker + "\n" + strings.Repeat("x", tt.restLen)
			id := p.AddComment(bot, previous)
			r, _ := reporter(t, c, "run-a", "")

			claim := r.Claim(context.Background())
			assert.Equal(t, claim.Claimed, tt.claimed)
			assert.Equal(t, claim.Prior, "unowned")
			body := p.Body(id)
			assert.Assert(t, fits(body))
			assert.Assert(t, !strings.Contains(body, bannerStart))
			assert.Equal(t, content(body), strings.Repeat("x", tt.restLen))
		})
	}
}

func TestResolveOwnership(t *testing.T) {
	own := Header{Run: "run-a", Comment: "pr", State: Fetching}.String()
	newer := Header{Run: "run-b", Comment: "pr", State: Clean}.String()
	tests := []struct {
		name    string
		sticky  string // second line of the bot's comment; "" means no comment
		claim   Claim
		proceed bool
	}{
		{"claimed and still owned", own, Claim{Claimed: true}, true},
		{"claimed but a newer run took over", newer, Claim{Claimed: true}, false},
		{"claim failed, nobody else claimed", "## old review", Claim{Prior: "unowned"}, true},
		{"claim failed, a newer run claimed", newer, Claim{Prior: "unowned"}, false},
		{"claim failed before reading", newer, Claim{}, true},
		{"claim reported an error but GitHub applied it", own, Claim{Prior: "none"}, true},
		{"comment deleted since", "", Claim{Claimed: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, p, c := setup(t)
			if tt.sticky != "" {
				p.AddComment(bot, Marker+"\n"+tt.sticky+"\nbody")
			}
			r, _ := reporter(t, c, "run-a", "")
			tt.claim.RunID = "run-a"

			_, proceed, err := r.Resolve(context.Background(), tt.claim)
			assert.NilError(t, err)
			assert.Equal(t, proceed, tt.proceed)
		})
	}
}

func TestConcurrentFirstRunsEachPublishToTheirOwnComment(t *testing.T) {
	_, p, c := setup(t)
	// Both runs saw no comment, so both created one.
	ids := map[string]int64{}
	for _, run := range []string{"run-a", "run-b"} {
		ids[run] = p.AddComment(bot, Marker+"\n"+Header{Run: run, Comment: "pr", State: Fetching}.String()+"\n")
	}
	for _, run := range []string{"run-a", "run-b"} {
		r, _ := reporter(t, c, run, "")
		st, proceed, err := r.Resolve(context.Background(), Claim{RunID: run, Claimed: true, Prior: "none"})
		assert.NilError(t, err)
		assert.Assert(t, proceed, run)
		assert.Equal(t, st.ID, ids[run])
	}
}

func TestFailLeavesANewerRunAlone(t *testing.T) {
	for _, commentID := range []string{"", "55"} {
		f, p, c := setup(t)
		p.AddComment(bot, Marker+"\n"+Header{Run: "run-b", Comment: "pr", State: Clean}.String()+"\nnewer review")
		r, log := reporter(t, c, "run-a", commentID)

		r.Fail(context.Background())
		assert.Equal(t, len(f.Writes()), 0, commentID)
		assert.Assert(t, strings.Contains(log.String(), "does not own"))
	}
}

func TestFailMakesNoWritesWhenTheCommentCannotBeRead(t *testing.T) {
	f, p, c := setup(t)
	p.Fail["GET /repos/owner/repo/issues/1/comments"] = 500
	r, _ := reporter(t, c, "run-a", "")

	r.Fail(context.Background())
	assert.Equal(t, len(f.Writes()), 0)
}

func TestRunCommandRejectsBadFlagsBeforeAnyRequest(t *testing.T) {
	f, c := ghtest.New(t)
	for _, opts := range []CommandOptions{
		{Repo: "owner/repo", PRNumber: 1, RunID: "Not Valid", State: "failed"},
		{Repo: "owner/repo", PRNumber: 1, RunID: "run-a", State: "clean"},
		{Repo: "owner/repo", RunID: "run-a", State: "failed"},
	} {
		opts.GitHub = c
		assert.Assert(t, RunCommand(context.Background(), opts) != nil, "%+v", opts)
	}
	assert.Equal(t, len(f.Calls("")), 0)
}

func TestProgressNeedsOwnership(t *testing.T) {
	tests := []struct {
		name   string
		sticky string
	}{
		{"a newer run took over", Header{Run: "run-b", Comment: "pr", State: Clean}.String()},
		{"this run never claimed the comment", "## old review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, p, c := setup(t)
			p.AddComment(bot, Marker+"\n"+tt.sticky+"\nbody")
			r, _ := reporter(t, c, "run-a", "55")

			r.Progress(context.Background(), Reviewing)
			assert.Equal(t, len(f.Writes()), 0)
		})
	}
}

func TestPublishCutsASummaryThatOnlyFitsWithoutTheHeader(t *testing.T) {
	_, p, c := setup(t)
	r, _ := reporter(t, c, "run-a", "")
	text := strings.Repeat("x", maxCommentChars-len(Marker)-1)

	st, err := r.Publish(context.Background(), nil, Marker+"\n"+text, Clean)
	assert.NilError(t, err)
	body := p.Body(st.ID)
	assert.Equal(t, utf8.RuneCountInString(body), maxCommentChars)
	h, ok := parseHeader(body)
	assert.Assert(t, ok)
	assert.Equal(t, h.Run, "run-a")
	assert.Assert(t, strings.HasSuffix(body, cutNote))
}
