package model

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

type recorded struct {
	Method string
	URL    string
	Header http.Header
	Body   map[string]any
}

type fakeTransport struct {
	mu      sync.Mutex
	reqs    []recorded
	respond func(*http.Request) *http.Response
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(data, &body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{Method: req.Method, URL: req.URL.String(), Header: req.Header.Clone(), Body: body})
	f.mu.Unlock()
	resp := f.respond(req)
	resp.Request = req
	return resp, nil
}

func response(code int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func sse(events ...string) string {
	var b strings.Builder
	for _, ev := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(ev), &probe)
		b.WriteString("event: " + probe.Type + "\ndata: " + ev + "\n\n")
	}
	return b.String()
}

const (
	evStart      = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`
	evBlockStart = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	evDelta      = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"summary\":\"ok\"}"}}`
	evBlockStop  = `{"type":"content_block_stop","index":0}`
	evStop       = `{"type":"message_stop"}`
)

func evMessageDelta(reason string) string {
	return `{"type":"message_delta","delta":{"stop_reason":"` + reason + `","stop_sequence":null},"usage":{"output_tokens":5}}`
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "VERTEX_LOCATION"} {
		t.Setenv(k, "")
	}
}

func writeCreds(t *testing.T, fields map[string]string) string {
	t.Helper()
	data, err := json.Marshal(fields)
	assert.NilError(t, err)
	p := filepath.Join(t.TempDir(), "sa.json")
	assert.NilError(t, os.WriteFile(p, data, 0o600))
	return p
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name    string
		creds   map[string]string
		path    string
		project string
		region  string
		wantErr string
	}{
		{name: "nothing configured", wantErr: "neither ANTHROPIC_API_KEY nor GOOGLE_APPLICATION_CREDENTIALS"},
		{name: "unreadable file", path: "/nonexistent/sa.json", wantErr: "could not read the Vertex AI credentials file"},
		{name: "missing fields", creds: map[string]string{"client_email": "a@b"}, wantErr: "missing required fields"},
		{name: "missing project", creds: map[string]string{"client_email": "a@b", "private_key": "k"}, wantErr: "no Vertex AI project"},
		{name: "bad region", creds: map[string]string{"client_email": "a@b", "private_key": "k", "project_id": "p"}, region: "x.evil/", wantErr: "invalid VERTEX_LOCATION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			p := tt.path
			if tt.creds != nil {
				p = writeCreds(t, tt.creds)
			}
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", p)
			t.Setenv("GOOGLE_CLOUD_PROJECT", tt.project)
			t.Setenv("VERTEX_LOCATION", tt.region)
			_, err := Resolve(context.Background(), Config{})
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestResolvePrefersAnthropicKey(t *testing.T) {
	clearEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeCreds(t, map[string]string{"client_email": "a@b"}))
	r, err := Resolve(context.Background(), Config{})
	assert.NilError(t, err)
	assert.Equal(t, r.Backend, BackendAnthropic)
	assert.Equal(t, r.DefaultModel, DefaultAnthropicModel)
	assert.DeepEqual(t, r.Secrets, []string{"sk-ant-test"})
}

func anthropicClient(t *testing.T, ft *fakeTransport) Client {
	t.Helper()
	clearEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_BASE_URL", "https://sentinel.invalid")
	r, err := Resolve(context.Background(), Config{Transport: ft})
	assert.NilError(t, err)
	return r.Client
}

func TestCompleteAnthropicRequest(t *testing.T) {
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
	}}
	c := anthropicClient(t, ft)

	schema := map[string]any{"type": "object"}
	res, err := c.Complete(context.Background(), Request{
		System: "sys", Prompt: "hello", Model: "claude-x", Effort: "high", Schema: schema, MaxTokens: 100,
	})
	assert.NilError(t, err)
	assert.Equal(t, res.Text, `{"summary":"ok"}`)

	assert.Equal(t, len(ft.reqs), 1)
	got := ft.reqs[0]
	assert.Equal(t, got.URL, "https://api.anthropic.com/v1/messages")
	assert.Equal(t, got.Header.Get("X-Api-Key"), "sk-ant-test")
	assert.Equal(t, got.Body["model"], "claude-x")
	assert.Equal(t, got.Body["max_tokens"], float64(100))
	assert.Equal(t, got.Body["stream"], true)
	assert.DeepEqual(t, got.Body["system"], []any{map[string]any{"type": "text", "text": "sys"}})
	assert.DeepEqual(t, got.Body["output_config"], map[string]any{
		"effort": "high",
		"format": map[string]any{"type": "json_schema", "schema": schema},
	})
	_, hasTools := got.Body["tools"]
	assert.Assert(t, !hasTools)
}

func TestCompleteWithoutOutputConfig(t *testing.T) {
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
	}}
	c := anthropicClient(t, ft)
	result, err := c.Complete(context.Background(), Request{
		Prompt: "Return review JSON", Model: "claude-haiku-4-5-20251001", MaxTokens: 100,
	})
	assert.NilError(t, err)
	assert.Equal(t, result.Text, `{"summary":"ok"}`)
	assert.Equal(t, len(ft.reqs), 1)
	_, hasConfig := ft.reqs[0].Body["output_config"]
	assert.Assert(t, !hasConfig, "plain requests must omit output_config")
}

func TestCompleteFailures(t *testing.T) {
	tests := []struct {
		name    string
		respond func(*http.Request) *http.Response
		wantErr string
		wantInc bool
	}{
		{
			name: "max tokens",
			respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("max_tokens"), evStop))
			},
			wantErr: "output token limit", wantInc: true,
		},
		{
			name: "refusal",
			respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", sse(evStart, evMessageDelta("refusal"), evStop))
			},
			wantErr: "refused", wantInc: true,
		},
		{
			name: "premature end of stream",
			respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta))
			},
			wantErr: "before message_stop", wantInc: true,
		},
		{
			name: "error event mid stream",
			respond: func(*http.Request) *http.Response {
				return response(200, "text/event-stream", sse(evStart, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			},
			wantErr: "Overloaded",
		},
		{
			name: "http error",
			respond: func(*http.Request) *http.Response {
				return response(400, "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"effort not supported"}}`)
			},
			wantErr: "effort not supported",
		},
		{
			name: "redirect to http refused",
			respond: func(r *http.Request) *http.Response {
				resp := response(302, "text/plain", "")
				resp.Header.Set("Location", "http://"+r.URL.Host+r.URL.Path)
				return resp
			},
			wantErr: "non-https redirect",
		},
		{
			name: "cross origin redirect refused",
			respond: func(r *http.Request) *http.Response {
				resp := response(307, "text/plain", "")
				resp.Header.Set("Location", "https://elsewhere.invalid/v1/messages")
				return resp
			},
			wantErr: "cross-origin redirect",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{respond: tt.respond}
			_, err := anthropicClient(t, ft).Complete(context.Background(), Request{Prompt: "p", Model: "m", MaxTokens: 10})
			assert.ErrorContains(t, err, tt.wantErr)
			var inc *IncompleteError
			assert.Equal(t, errors.As(err, &inc), tt.wantInc)
			assert.Equal(t, len(ft.reqs), 1, "no automatic retries")
		})
	}
}

func TestCompleteCancelled(t *testing.T) {
	ft := &fakeTransport{respond: func(*http.Request) *http.Response {
		return response(200, "text/event-stream", sse(evStart))
	}}
	c := anthropicClient(t, ft)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Complete(ctx, Request{Prompt: "p", Model: "m", MaxTokens: 10})
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
}

func vertexTestCredentials(t *testing.T, tokenURL string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NilError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	assert.NilError(t, err)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	clearEnv(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeCreds(t, map[string]string{
		"type": "service_account", "client_email": "sa@proj.iam.gserviceaccount.com",
		"private_key": pemKey, "project_id": "from-file", "token_uri": tokenURL,
	}))
}

func TestCompleteVertexRequest(t *testing.T) {
	vertexTestCredentials(t, "https://oauth2.invalid/token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://sentinel.invalid")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "proj")
	t.Setenv("VERTEX_LOCATION", "us-east5")

	ft := &fakeTransport{respond: func(r *http.Request) *http.Response {
		if r.URL.Host == "oauth2.invalid" {
			return response(200, "application/json", `{"access_token":"vertex-token","token_type":"Bearer","expires_in":3600}`)
		}
		return response(200, "text/event-stream", sse(evStart, evBlockStart, evDelta, evBlockStop, evMessageDelta("end_turn"), evStop))
	}}
	r, err := Resolve(context.Background(), Config{Transport: ft})
	assert.NilError(t, err)
	assert.Equal(t, r.Backend, BackendVertex)
	assert.DeepEqual(t, r.Secrets, []string{"sa@proj.iam.gserviceaccount.com"})

	res, err := r.Client.Complete(context.Background(), Request{Prompt: "p", Model: DefaultVertexModel, Effort: "low", MaxTokens: 10})
	assert.NilError(t, err)
	assert.Equal(t, res.Text, `{"summary":"ok"}`)

	var model recorded
	for _, rec := range ft.reqs {
		if !strings.Contains(rec.URL, "oauth2.invalid") {
			model = rec
		}
	}
	assert.Equal(t, model.URL, "https://us-east5-aiplatform.googleapis.com/v1/projects/proj/locations/us-east5/publishers/anthropic/models/claude-opus-4-6@default:streamRawPredict")
	assert.Equal(t, model.Header.Get("Authorization"), "Bearer vertex-token")
	_, hasModel := model.Body["model"]
	assert.Assert(t, !hasModel)
	assert.Equal(t, model.Body["anthropic_version"], "vertex-2023-10-16")
	assert.Assert(t, !bytes.Contains([]byte(model.URL), []byte("sentinel")))
}

func TestTokenTransportPreservesHTTPTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := &http.Client{
		Timeout: 50 * time.Millisecond,
		Transport: &tokenTransport{
			ctx:  context.Background(),
			base: server.Client().Transport,
		},
	}
	resp, err := client.Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.Assert(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
}

func TestVertexTokenRequestCancellation(t *testing.T) {
	tests := []struct {
		name     string
		deadline bool
		body     bool
	}{
		{name: "cancel before headers"},
		{name: "cancel during body", body: true},
		{name: "deadline before headers", deadline: true},
		{name: "deadline during body", deadline: true, body: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, r.URL.Path, "/token")
				_, _ = io.Copy(io.Discard, r.Body)
				if tt.body {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			vertexTestCredentials(t, server.URL+"/token")

			var ctx context.Context
			var cancel context.CancelFunc
			if tt.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			resolved, err := Resolve(ctx, Config{Transport: server.Client().Transport})
			assert.NilError(t, err)
			done := make(chan error, 1)
			go func() {
				_, err := resolved.Client.Complete(ctx, Request{Prompt: "p", Model: DefaultVertexModel, MaxTokens: 10})
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("token request did not start")
			}
			if !tt.deadline {
				cancel()
			}
			select {
			case err := <-done:
				assert.Assert(t, err != nil)
				assert.Assert(t, ctx.Err() != nil)
			case <-time.After(3 * time.Second):
				t.Fatal("stalled token request ignored review cancellation")
			}
		})
	}
}
