package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCall is one GraphQL request the fake received.
type fakeCall struct {
	Query string
	Vars  map[string]any
}

// str returns a string variable, or "" when it is absent.
func (c fakeCall) str(name string) string {
	s, _ := c.Vars[name].(string)
	return s
}

// fakeReply is the HTTP response the fake sends back. A zero Status means 200.
type fakeReply struct {
	Status int
	Header http.Header
	Body   string
}

// dataReply wraps data in a GraphQL {"data": ...} envelope.
func dataReply(t *testing.T, data any) fakeReply {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatalf("marshal fake reply: %v", err)
	}
	return fakeReply{Body: string(b)}
}

// fakeGitHub stands in for the GraphQL endpoint: it decodes each request,
// asks handle for the reply, and records the calls and rate-limit sleeps.
type fakeGitHub struct {
	t      *testing.T
	handle func(fakeCall) fakeReply

	mu     sync.Mutex
	calls  []fakeCall
	sleeps []time.Duration
}

// newFakeClient returns a client wired to a fakeGitHub. Rate-limit sleeps are
// recorded instead of waited out, so tests run instantly.
func newFakeClient(t *testing.T, handle func(fakeCall) fakeReply) (*Client, *fakeGitHub) {
	t.Helper()
	f := &fakeGitHub{t: t, handle: handle}
	c := NewClient("test-token")
	c.http = &http.Client{Transport: f}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		f.mu.Lock()
		f.sleeps = append(f.sleeps, d)
		f.mu.Unlock()
		return ctx.Err()
	}
	return c, f
}

// RoundTrip implements http.RoundTripper.
func (f *fakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var gr gqlRequest
	if err := json.Unmarshal(raw, &gr); err != nil {
		f.t.Errorf("fake GitHub: undecodable request body %q: %v", raw, err)
	}
	call := fakeCall{Query: gr.Query, Vars: gr.Variables}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	reply := f.handle(call)
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	header := reply.Header
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(reply.Body)),
		Request:    req,
	}, nil
}

// callsFor returns the recorded calls that ran the given query.
func (f *fakeGitHub) callsFor(query string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Query == query {
			out = append(out, c)
		}
	}
	return out
}

// repoJSON builds a repository node as the GraphQL API returns it.
func repoJSON(owner, name, lang string, bytes int64) map[string]any {
	r := map[string]any{
		"name":      name,
		"owner":     map[string]any{"login": owner},
		"isPrivate": false,
		"isFork":    false,
		"languages": map[string]any{"edges": []any{}},
	}
	if lang != "" {
		r["primaryLanguage"] = map[string]any{"name": lang, "color": "#" + lang}
		r["languages"] = map[string]any{"edges": []any{
			map[string]any{"size": bytes, "node": map[string]any{"name": lang, "color": "#" + lang}},
		}}
	}
	return r
}
