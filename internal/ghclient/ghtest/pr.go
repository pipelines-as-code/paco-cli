package ghtest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Comment is a top-level comment on the simulated pull request.
type Comment struct {
	ID    int64  `json:"id"`
	Login string `json:"-"`
	Body  string `json:"body"`
}

// Reaction is a reaction on the simulated pull request or one of its comments.
type Reaction struct {
	ID      int64  `json:"id"`
	Login   string `json:"-"`
	Content string `json:"content"`
}

// PR simulates the comments, reactions and labels of owner/repo#1, with Bot
// as the token's owner. It takes precedence over routes registered with
// Handle, except for keys listed in Fail.
type PR struct {
	mu        sync.Mutex
	Bot       string
	Comments  []Comment
	Reactions map[string][]Reaction // "pr" or the comment ID
	Labels    []string
	// Fail answers "METHOD /path" with the given status code.
	Fail   map[string]int
	nextID int64
}

// PR installs a simulated pull request whose token owner is bot. An empty
// bot makes the viewer query fail.
func (f *Fake) PR(bot string) *PR {
	p := &PR{Bot: bot, Reactions: map[string][]Reaction{}, Fail: map[string]int{}, nextID: 1000}
	f.mu.Lock()
	f.pr = p
	f.mu.Unlock()
	return p
}

// AddComment seeds a comment and returns its ID.
func (p *PR) AddComment(login, body string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	p.Comments = append(p.Comments, Comment{ID: p.nextID, Login: login, Body: body})
	return p.nextID
}

// SetBody replaces a comment's body, as another run would.
func (p *PR) SetBody(id int64, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Comments {
		if p.Comments[i].ID == id {
			p.Comments[i].Body = body
		}
	}
}

// AddReaction seeds a reaction on target ("pr" or a comment ID).
func (p *PR) AddReaction(target, login, content string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	p.Reactions[target] = append(p.Reactions[target], Reaction{ID: p.nextID, Login: login, Content: content})
}

// Body returns the comment's current body.
func (p *PR) Body(id int64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.Comments {
		if c.ID == id {
			return c.Body
		}
	}
	return ""
}

// ReactionsBy lists the contents of login's reactions on target.
func (p *PR) ReactionsBy(target, login string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.Reactions[target] {
		if r.Login == login {
			out = append(out, r.Content)
		}
	}
	return out
}

const (
	repoPath  = "/repos/owner/repo"
	issuePath = repoPath + "/issues/1"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func user(login string) map[string]string { return map[string]string{"login": login} }

func (p *PR) serve(w http.ResponseWriter, r *http.Request, body string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := r.URL.Path
	if code, ok := p.Fail[r.Method+" "+path]; ok {
		writeJSON(w, code, map[string]string{"message": "injected failure"})
		return true
	}
	switch {
	case r.Method == http.MethodPost && path == "/graphql":
		if !strings.Contains(body, "viewer") {
			return false
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"viewer": user(p.Bot)}})
	case path == issuePath+"/comments":
		p.comments(w, r)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, repoPath+"/issues/comments/") && !strings.Contains(path, "/reactions"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, repoPath+"/issues/comments/"), 10, 64)
		var req struct{ Body string }
		_ = json.Unmarshal([]byte(body), &req)
		for i := range p.Comments {
			if p.Comments[i].ID == id {
				p.Comments[i].Body = req.Body
				writeJSON(w, http.StatusOK, map[string]any{"id": id, "body": req.Body, "user": user(p.Comments[i].Login)})
				return true
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, repoPath+"/issues/comments/") && !strings.Contains(path, "/reactions"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, repoPath+"/issues/comments/"), 10, 64)
		p.Comments = slices.DeleteFunc(p.Comments, func(c Comment) bool { return c.ID == id })
		w.WriteHeader(http.StatusNoContent)
	case strings.Contains(path, "/reactions"):
		return p.reactions(w, r, body)
	case strings.HasPrefix(path, issuePath+"/labels"), r.Method == http.MethodPost && path == repoPath+"/labels":
		p.labels(w, r, body)
	default:
		return false
	}
	return true
}

func (p *PR) comments(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		p.nextID++
		p.Comments = append(p.Comments, Comment{ID: p.nextID, Login: p.Bot, Body: req.Body})
		writeJSON(w, http.StatusCreated, map[string]any{"id": p.nextID, "body": req.Body, "user": user(p.Bot)})
		return
	}
	out := []map[string]any{}
	for _, c := range p.Comments {
		out = append(out, map[string]any{"id": c.ID, "body": c.Body, "user": user(c.Login)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *PR) reactions(w http.ResponseWriter, r *http.Request, body string) bool {
	path := r.URL.Path
	var target, rest string
	switch {
	case strings.HasPrefix(path, issuePath+"/reactions"):
		target, rest = "pr", strings.TrimPrefix(path, issuePath+"/reactions")
	case strings.HasPrefix(path, repoPath+"/issues/comments/"):
		id, after, _ := strings.Cut(strings.TrimPrefix(path, repoPath+"/issues/comments/"), "/reactions")
		target, rest = id, after
	default:
		return false
	}
	toJSON := func(rc Reaction) map[string]any {
		return map[string]any{"id": rc.ID, "content": rc.Content, "user": user(rc.Login)}
	}
	switch r.Method {
	case http.MethodPost:
		var req struct{ Content string }
		_ = json.Unmarshal([]byte(body), &req)
		for _, rc := range p.Reactions[target] {
			if rc.Login == p.Bot && rc.Content == req.Content {
				writeJSON(w, http.StatusOK, toJSON(rc))
				return true
			}
		}
		p.nextID++
		rc := Reaction{ID: p.nextID, Login: p.Bot, Content: req.Content}
		p.Reactions[target] = append(p.Reactions[target], rc)
		writeJSON(w, http.StatusCreated, toJSON(rc))
	case http.MethodGet:
		out := []map[string]any{}
		for _, rc := range p.Reactions[target] {
			out = append(out, toJSON(rc))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(strings.TrimPrefix(rest, "/"), 10, 64)
		kept := p.Reactions[target][:0]
		for _, rc := range p.Reactions[target] {
			if rc.ID != id {
				kept = append(kept, rc)
			}
		}
		p.Reactions[target] = kept
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

func (p *PR) labels(w http.ResponseWriter, r *http.Request, body string) {
	path := r.URL.Path
	switch {
	case path == repoPath+"/labels":
		writeJSON(w, http.StatusCreated, map[string]any{})
	case r.Method == http.MethodPost:
		var names []string
		_ = json.Unmarshal([]byte(body), &names)
		for _, n := range names {
			if !p.hasLabel(n) {
				p.Labels = append(p.Labels, n)
			}
		}
		p.listLabels(w)
	case r.Method == http.MethodDelete:
		name := strings.TrimPrefix(path, issuePath+"/labels/")
		if !p.hasLabel(name) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Label does not exist"})
			return
		}
		kept := p.Labels[:0]
		for _, l := range p.Labels {
			if l != name {
				kept = append(kept, l)
			}
		}
		p.Labels = kept
		p.listLabels(w)
	default:
		p.listLabels(w)
	}
}

func (p *PR) hasLabel(name string) bool {
	for _, l := range p.Labels {
		if l == name {
			return true
		}
	}
	return false
}

func (p *PR) listLabels(w http.ResponseWriter) {
	out := []map[string]string{}
	for _, l := range p.Labels {
		out = append(out, map[string]string{"name": l})
	}
	writeJSON(w, http.StatusOK, out)
}
