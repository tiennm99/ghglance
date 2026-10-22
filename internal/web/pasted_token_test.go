package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tiennm99/ghglance/internal/github"
)

// assertTokenNowhere fails when token reached a file under the data dir or
// the log.
func assertTokenNowhere(t *testing.T, s *Server, logs *lockedBuffer, token string) {
	t.Helper()
	err := filepath.WalkDir(s.store.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		raw, _ := os.ReadFile(path)
		if bytes.Contains(raw, []byte(token)) {
			t.Errorf("token written to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	if strings.Contains(logs.String(), token) {
		t.Errorf("log contains the token:\n%s", logs.String())
	}
}

func TestPastedTokenTakesPrecedence(t *testing.T) {
	logs := captureLog(t)
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{testToken: {Login: "octocat", CanReadPrivate: true}}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	publishTest(t, s.store, "octocat") // in cooldown: the owner's token skips it
	// The form posts the same way whichever button is pressed, Enter in
	// the token field included: a non-empty token wins over signing in.
	rec := startSignIn(h, url.Values{
		"user": {"OctoCat"}, "include_private": {"1"}, "token": {" " + testToken + " "},
	}, "203.0.113.60")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/u/octocat" {
		t.Fatalf("pasted token post = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if c := rec.Result().Cookies(); len(c) != 0 {
		t.Errorf("pasted token started a sign-in: cookies %v", c)
	}
	if st := waitIdle(t, s.queue, "octocat"); st.State != stateDone {
		t.Fatalf("job = %+v", st)
	}
	call := f.lastCall()
	if call.token != testToken || !call.cfg.Options.IncludePrivate || !call.cfg.Strict {
		t.Errorf("fetch = %+v", call)
	}
	if m, err := s.store.Meta("octocat"); err != nil || m.Scope != "private" {
		t.Errorf("meta = %+v, %v", m, err)
	}
	if got := g.revokedTokens(); len(got) != 0 {
		t.Errorf("pasted token revoked: %q", got)
	}
	if g.exchangeCount() != 0 {
		t.Error("pasted token talked to the OAuth token endpoint")
	}
	s.logins.mu.Lock()
	pending := len(s.logins.entries)
	s.logins.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d sign-ins parked for a pasted token", pending)
	}
	s.queue.mu.Lock()
	leftover := s.queue.jobs["octocat"].token
	s.queue.mu.Unlock()
	if leftover != "" {
		t.Error("pasted token kept in memory after the job ended")
	}
	assertTokenNowhere(t, s, logs, testToken)
}

func TestPastedTokenForeignAccount(t *testing.T) {
	logs := captureLog(t)
	const publicToken = "ghp_publicONLYvalue0123456789abcdef"
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{
		testToken:   {Login: "alice", CanReadPrivate: true},
		publicToken: {Login: "alice"},
	}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()
	post := func(token, ip string) {
		t.Helper()
		rec := startSignIn(h, url.Values{
			"user": {"xavier"}, "include_private": {"1"}, "include_org_repos": {"1"}, "token": {token},
		}, ip)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("post = %d: %s", rec.Code, rec.Body.String())
		}
	}

	// A token that can read private repos is only good for its owner.
	post(testToken, "203.0.113.70")
	if st := waitIdle(t, s.queue, "xavier"); st.State != stateFailed || st.Error != errForeignPrivatePasted.Error() || strings.Contains(st.Error, "alice") {
		t.Fatalf("private-capable foreign token = %+v; want a refusal that does not name the token's account", st)
	}
	if f.callCount() != 0 {
		t.Fatal("fetched another user with a private-capable token")
	}

	// A public-only token for another account renders public data...
	post(publicToken, "203.0.113.71")
	if st := waitIdle(t, s.queue, "xavier"); st.State != stateDone {
		t.Fatalf("public foreign token = %+v", st)
	}
	if o := f.lastCall().cfg.Options; o.IncludePrivate || o.IncludeOrgRepos {
		t.Errorf("foreign token kept private scope: %+v", o)
	}
	if m, err := s.store.Meta("xavier"); err != nil || m.Scope != "public" || m.Options.IncludePrivate {
		t.Errorf("meta = %+v, %v", m, err)
	}

	// ...under the cooldown.
	post(publicToken, "203.0.113.72")
	if st := waitIdle(t, s.queue, "xavier"); st.State != stateFailed || st.Error != errFreshPasted.Error() {
		t.Fatalf("foreign token during cooldown = %+v", st)
	}
	if n := f.callCount(); n != 1 {
		t.Errorf("fetched %d times, want 1", n)
	}
	if got := g.revokedTokens(); len(got) != 0 {
		t.Errorf("pasted tokens revoked: %q", got)
	}
	assertTokenNowhere(t, s, logs, testToken)
	assertTokenNowhere(t, s, logs, publicToken)
}

func TestPastedTokenNeverEchoed(t *testing.T) {
	s := newTestServer(t, &fakeFetcher{})
	h := s.Handler()

	for name, v := range map[string]url.Values{
		"invalid user":  {"user": {"--bad"}, "token": {testToken}},
		"invalid tz":    {"user": {"octocat"}, "tz": {"Mars/Olympus"}, "token": {testToken}},
		"invalid token": {"user": {"octocat"}, "token": {testToken + "\r\nX-Evil: 1"}},
	} {
		rec := startSignIn(h, v, "203.0.113.80")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, testToken) {
			t.Errorf("%s: token echoed back into the page", name)
		}
	}

	// The same rate limit covers pasted tokens.
	var last int
	for range submitBurst + 1 {
		last = startSignIn(h, url.Values{"user": {"--bad"}, "token": {testToken}}, "203.0.113.81").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("pasted-token post past burst = %d, want 429", last)
	}
}

func TestPastedTokenNotRevokedOnShutdown(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{gate: make(chan struct{})}
	s := newOAuthTestServer(t, f, g)
	for _, login := range []string{"a1", "a2", "a3"} {
		if _, err := s.queue.Submit(submission{Login: login, Token: testToken, Pasted: true}); err != nil {
			t.Fatal(err)
		}
	}
	s.queue.Stop() // two running, one still queued
	if got := g.revokedTokens(); len(got) != 0 {
		t.Errorf("revoked %d pasted tokens on shutdown, want 0", len(got))
	}
}

func TestPastedOwnTokenScopeFollowsTick(t *testing.T) {
	logs := captureLog(t)
	const publicToken = "ghp_publicONLYvalue0123456789abcdef"
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{
		testToken:   {Login: "octocat", CanReadPrivate: true},
		publicToken: {Login: "octocat"},
	}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	// GitHub folds private contributions into the totals for a token that
	// can see them, so an unticked box cannot keep them off the cards.
	rec := startSignIn(h, url.Values{"user": {"octocat"}, "token": {testToken}}, "203.0.113.90")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("post = %d: %s", rec.Code, rec.Body.String())
	}
	if st := waitIdle(t, s.queue, "octocat"); st.State != stateFailed || st.Error != errOwnPrivateUntickedPasted.Error() {
		t.Fatalf("own private-capable token, private unticked = %+v", st)
	}
	if f.callCount() != 0 {
		t.Fatal("fetched with a private-capable token while private repos were unticked")
	}

	// A token that cannot read private repos yields public data, ticked
	// or not.
	rec = startSignIn(h, url.Values{"user": {"octocat"}, "include_private": {"1"}, "token": {publicToken}}, "203.0.113.91")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("post = %d: %s", rec.Code, rec.Body.String())
	}
	if st := waitIdle(t, s.queue, "octocat"); st.State != stateDone {
		t.Fatalf("own public token = %+v", st)
	}
	if m, err := s.store.Meta("octocat"); err != nil || m.Scope != "public" {
		t.Errorf("meta = %+v, %v; want public scope", m, err)
	}
	assertTokenNowhere(t, s, logs, testToken)
	assertTokenNowhere(t, s, logs, publicToken)
}
