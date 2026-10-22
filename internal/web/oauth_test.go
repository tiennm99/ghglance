package web

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tiennm99/ghglance/internal/github"
)

const (
	testClientID     = "Iv1testclientid"
	testClientSecret = "test-client-secret-value"
	testPublicURL    = "https://ghglance.example"
	testCode         = "good-code"
	// oauthToken is what the fake GitHub issues for testCode.
	oauthToken = "gho_oauthTOKENvalue0123456789abcdef"
)

// fakeGitHub plays GitHub's OAuth token and revoke endpoints.
type fakeGitHub struct {
	srv *httptest.Server

	mu        sync.Mutex
	scope     string // scopes granted with the token; "" grants what was asked
	fail      string // token endpoint failure: "status" (HTTP 500) or "garbage" (not JSON)
	asked     string // scopes the last sign-in asked for
	exchanges []url.Values
	revoked   []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.exchanges = append(g.exchanges, r.PostForm)
		scope := cmp.Or(g.scope, strings.ReplaceAll(g.asked, " ", ","))
		fail := g.fail
		g.mu.Unlock()
		switch fail {
		case "status":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "garbage":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>unicorn</html>")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		f := r.PostForm
		if r.Header.Get("Accept") != "application/json" || f.Get("client_id") != testClientID ||
			f.Get("client_secret") != testClientSecret || f.Get("code") != testCode ||
			f.Get("redirect_uri") != testPublicURL+"/auth/callback" || f.Get("code_verifier") == "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": oauthToken, "token_type": "bearer", "scope": scope})
	})
	mux.HandleFunc("DELETE /applications/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != testClientID || secret != testClientSecret || r.PathValue("id") != testClientID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.revoked = append(g.revoked, body.AccessToken)
		g.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) exchangeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.exchanges)
}

func (g *fakeGitHub) revokedTokens() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.revoked...)
}

func newOAuthTestServer(t *testing.T, f *fakeFetcher, g *fakeGitHub) *Server {
	t.Helper()
	return newServerWith(t, f, g, time.Minute)
}

// startSignIn posts the form to /auth/start from ip.
func startSignIn(h http.Handler, v url.Values, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = ip + ":1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// signIn starts a sign-in and returns GitHub's authorize query and the
// state cookie. The fake GitHub remembers the scopes asked for.
func signIn(t *testing.T, h http.Handler, g *fakeGitHub, v url.Values) (url.Values, *http.Cookie) {
	t.Helper()
	rec := startSignIn(h, v, "203.0.113.50")
	if rec.Code != http.StatusFound {
		t.Fatalf("auth start = %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.asked = loc.Query().Get("scope")
	g.mu.Unlock()
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthCookie {
			return loc.Query(), c
		}
	}
	t.Fatal("no state cookie set")
	return nil, nil
}

// callback calls /auth/callback with query and an optional cookie.
func callback(h http.Handler, query url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?"+query.Encode(), nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// lockedBuffer collects log output written from several goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

func TestOAuthScopesFollowTicks(t *testing.T) {
	cases := []struct {
		private, org bool
		want         string
	}{
		{false, false, "read:user"},
		{false, true, "read:user"},
		{true, false, "repo read:user"},
		{true, true, "repo read:user read:org"},
	}
	for _, c := range cases {
		if got := oauthScopes(Options{IncludePrivate: c.private, IncludeOrgRepos: c.org}); got != c.want {
			t.Errorf("oauthScopes(private=%v, org=%v) = %q, want %q", c.private, c.org, got, c.want)
		}
	}
}

func TestGrantScopesDowngrades(t *testing.T) {
	both := Options{IncludePrivate: true, IncludeOrgRepos: true}
	cases := []struct {
		asked   Options
		granted string
		want    Options
		dropped string
	}{
		{both, "repo,read:user,read:org", both, ""},
		{both, "read:org, repo, user", both, ""},
		{both, "repo,read:user", Options{IncludePrivate: true}, "org"},
		{both, "admin:org,repo", both, ""},
		{both, "read:user", Options{}, "private"},
		{Options{IncludePrivate: true}, "", Options{}, "private"},
		{Options{IncludeOrgRepos: true}, "read:user", Options{IncludeOrgRepos: true}, ""},
	}
	for _, c := range cases {
		got, dropped := grantScopes(c.asked, c.granted)
		if got != c.want || dropped != c.dropped {
			t.Errorf("grantScopes(%+v, %q) = %+v, %q; want %+v, %q", c.asked, c.granted, got, dropped, c.want, c.dropped)
		}
	}
}

func TestOAuthStartRedirectsToGitHub(t *testing.T) {
	g := newFakeGitHub(t)
	s := newOAuthTestServer(t, &fakeFetcher{}, g)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := rec.Body.String(); !strings.Contains(body, `action="/auth/start"`) || !strings.Contains(body, "Sign in with GitHub") {
		t.Error("index lacks the sign-in form")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+g.srv.URL+";") {
		t.Errorf("CSP does not allow the authorize redirect: %s", csp)
	}

	if body := rec.Body.String(); strings.Contains(body, `name="include_private" value="1" checked`) {
		t.Error("private repos start ticked")
	}

	q, c := signIn(t, h, g, url.Values{
		"user": {"octocat"}, "include_private": {"1"}, "include_org_repos": {"1"},
		"token": {"  "}, // a blank token field still signs in
	})
	want := map[string]string{
		"client_id":             testClientID,
		"redirect_uri":          testPublicURL + "/auth/callback",
		"scope":                 "repo read:user read:org",
		"code_challenge_method": "S256",
		"login":                 "octocat",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("authorize %s = %q, want %q", k, q.Get(k), v)
		}
	}
	if raw, err := base64.RawURLEncoding.DecodeString(q.Get("state")); err != nil || len(raw) < 16 {
		t.Errorf("state %q is not at least 128 random bits", q.Get("state"))
	}
	if q.Get("code_challenge") == "" || q.Get("client_secret") != "" {
		t.Errorf("authorize query = %v", q)
	}
	if c.Value != q.Get("state") || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 600 {
		t.Errorf("state cookie = %+v", c)
	}

	// Public data only asks for read:user, whatever the org box says.
	q, _ = signIn(t, h, g, url.Values{"user": {"octocat"}, "include_org_repos": {"1"}})
	if q.Get("scope") != "read:user" {
		t.Errorf("public sign-in scope = %q", q.Get("scope"))
	}

	// The form is validated and rate limited before anything goes to GitHub.
	if rec := startSignIn(h, url.Values{"user": {"--bad"}}, "203.0.113.51"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid user = %d", rec.Code)
	}
	var last int
	for range submitBurst + 1 {
		last = startSignIn(h, url.Values{"user": {"octocat"}}, "203.0.113.52").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("sign-in past burst = %d, want 429", last)
	}
	if g.exchangeCount() != 0 {
		t.Error("starting a sign-in talked to the token endpoint")
	}
}

func TestOAuthFlowRevokesAfterJob(t *testing.T) {
	logs := captureLog(t)
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{oauthToken: {Login: "octocat", CanReadPrivate: true}}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	publishTest(t, s.store, "octocat") // in cooldown: a sign-in skips it
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}, "tz": {"Asia/Saigon"}, "include_private": {"1"}, "commits_per_repo": {"0"}})
	rec := callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/u/octocat" {
		t.Fatalf("callback = %d %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if st := waitIdle(t, s.queue, "octocat"); st.State != stateDone {
		t.Fatalf("job = %+v", st)
	}

	// PKCE: the verifier sent to the token endpoint hashes to the challenge.
	g.mu.Lock()
	verifier := g.exchanges[0].Get("code_verifier")
	g.mu.Unlock()
	if len(verifier) < 43 || pkceChallenge(verifier) != q.Get("code_challenge") {
		t.Errorf("verifier %q does not match challenge %q", verifier, q.Get("code_challenge"))
	}

	call := f.lastCall()
	if call.token != oauthToken || !call.cfg.Options.IncludePrivate || call.cfg.CommitsPerRepo != 0 {
		t.Errorf("fetch = %+v", call)
	}
	if m, err := s.store.Meta("octocat"); err != nil || m.Scope != "private" || m.Options.TZ != "Asia/Saigon" {
		t.Errorf("meta = %+v, %v", m, err)
	}
	if got := g.revokedTokens(); len(got) != 1 || got[0] != oauthToken {
		t.Errorf("revoked = %q, want the sign-in token once", got)
	}

	// The token, code and client secret never reach disk or the log.
	walkErr := filepath.WalkDir(s.store.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		raw, _ := os.ReadFile(path)
		if bytes.Contains(raw, []byte(oauthToken)) || bytes.Contains(raw, []byte(testClientSecret)) {
			t.Errorf("secret written to %s", path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	out := logs.String()
	for _, secret := range []string{oauthToken, testClientSecret, testCode, verifier} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q:\n%s", secret, out)
		}
	}
}

func TestOAuthStateChecks(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{oauthToken: {Login: "octocat"}}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}})
	state := q.Get("state")
	other := &http.Cookie{Name: oauthCookie, Value: state[:len(state)-1] + "x"}
	planted := &http.Cookie{Name: oauthCookieInsecure, Value: state}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no cookie":       callback(h, url.Values{"code": {testCode}, "state": {state}}, nil),
		"other cookie":    callback(h, url.Values{"code": {testCode}, "state": {state}}, other),
		"unprefixed name": callback(h, url.Values{"code": {testCode}, "state": {state}}, planted),
		"no state":        callback(h, url.Values{"code": {testCode}}, c),
		"unknown state":   callback(h, url.Values{"code": {testCode}, "state": {other.Value}}, other),
		"error, no state": callback(h, url.Values{"error": {"access_denied"}}, nil),
	} {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: callback = %d, want 400", name, rec.Code)
		}
	}
	if g.exchangeCount() != 0 {
		t.Fatal("a rejected callback reached the token endpoint")
	}

	// The real state still works once, then is spent.
	if rec := callback(h, url.Values{"code": {testCode}, "state": {state}}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("valid callback = %d", rec.Code)
	}
	waitIdle(t, s.queue, "octocat")
	if rec := callback(h, url.Values{"code": {testCode}, "state": {state}}, c); rec.Code != http.StatusBadRequest {
		t.Errorf("replayed callback = %d, want 400", rec.Code)
	}
	if n := g.exchangeCount(); n != 1 {
		t.Errorf("token endpoint called %d times, want 1", n)
	}
}

func TestOAuthStateExpires(t *testing.T) {
	g := newFakeGitHub(t)
	s := newOAuthTestServer(t, &fakeFetcher{}, g)
	h := s.Handler()
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}})
	s.logins.now = func() time.Time { return time.Now().Add(loginTTL + time.Second) }
	if rec := callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c); rec.Code != http.StatusBadRequest {
		t.Errorf("expired callback = %d, want 400", rec.Code)
	}
	if g.exchangeCount() != 0 {
		t.Error("expired sign-in reached the token endpoint")
	}
}

func TestCloseRevokesTokenOfCallbackInFlight(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokenStall: make(chan struct{})}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
	}()
	<-f.tokenStall // the callback holds the token and waits on GitHub
	s.Close()
	if got := g.revokedTokens(); len(got) != 1 || got[0] != oauthToken {
		t.Errorf("revoked = %q after Close, want the in-flight sign-in token", got)
	}
	<-done
}

func TestPendingLoginsEvictEarliestWhenFull(t *testing.T) {
	l := newPendingLogins()
	now := time.Now()
	l.now = func() time.Time { return now }
	var first string
	for i := range maxPendingLogins {
		state, err := l.add(&pendingLogin{client: strconv.Itoa(i)}, "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = state
		}
		now = now.Add(time.Millisecond)
	}
	state, err := l.add(&pendingLogin{client: "late"}, "")
	if err != nil {
		t.Fatalf("add past the cap = %v, want the earliest sign-in evicted", err)
	}
	if len(l.entries) != maxPendingLogins {
		t.Errorf("%d entries, want the cap %d", len(l.entries), maxPendingLogins)
	}
	if l.take(first) != nil {
		t.Error("the earliest-expiring sign-in survived a full table")
	}
	if l.take(state) == nil {
		t.Error("the newest sign-in was not stored")
	}

	// Expired sign-ins are dropped before anything live is evicted.
	now = now.Add(loginTTL)
	if _, err := l.add(&pendingLogin{}, ""); err != nil {
		t.Fatal(err)
	}
	if len(l.entries) != 1 {
		t.Errorf("%d entries left, want 1", len(l.entries))
	}
}

func TestPendingLoginsPerClientCap(t *testing.T) {
	l := newPendingLogins()
	now := time.Now()
	l.now = func() time.Time { return now }
	var states []string
	for range maxPendingPerClient {
		state, err := l.add(&pendingLogin{client: "203.0.113.7"}, "")
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
		now = now.Add(time.Second)
	}
	// Another browser behind the same address is refused; it never evicts
	// a neighbour's sign-in.
	if _, err := l.add(&pendingLogin{client: "203.0.113.7"}, ""); !errors.Is(err, errTooManyLogins) {
		t.Fatalf("add past the per-client cap = %v, want errTooManyLogins", err)
	}
	// A browser starting again replaces its own earlier sign-in.
	again, err := l.add(&pendingLogin{client: "203.0.113.7"}, states[0])
	if err != nil {
		t.Fatalf("restart from the same browser = %v", err)
	}
	other, err := l.add(&pendingLogin{client: "198.51.100.9"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.entries) != maxPendingPerClient+1 {
		t.Errorf("%d entries, want %d for the busy client plus one", len(l.entries), maxPendingPerClient+1)
	}
	if l.take(states[0]) != nil {
		t.Error("the replaced sign-in survived")
	}
	for i, state := range append(states[1:], again, other) {
		if l.take(state) == nil {
			t.Errorf("sign-in %d was evicted", i)
		}
	}
}

func TestOAuthStartEvictsWhenPendingFull(t *testing.T) {
	g := newFakeGitHub(t)
	s := newOAuthTestServer(t, &fakeFetcher{}, g)
	h := s.Handler()
	for i := range maxPendingLogins {
		if _, err := s.logins.add(&pendingLogin{client: "filler-" + strconv.Itoa(i)}, ""); err != nil {
			t.Fatal(err)
		}
	}
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}})
	if c.Value != q.Get("state") {
		t.Fatal("state cookie does not match the authorize state")
	}
	s.logins.mu.Lock()
	n := len(s.logins.entries)
	_, stored := s.logins.entries[q.Get("state")]
	s.logins.mu.Unlock()
	if n != maxPendingLogins || !stored {
		t.Errorf("pending table = %d entries, new sign-in stored = %v", n, stored)
	}
}

func TestOAuthStartRedirectsWhileJobActive(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{gate: make(chan struct{})}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()
	if _, err := s.queue.Submit(signedIn(t, testToken, "user", "octocat")); err != nil {
		t.Fatal(err)
	}
	rec := startSignIn(h, url.Values{"user": {"octocat"}}, "203.0.113.60")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/u/octocat?notice=pending" {
		t.Errorf("start during a job = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("start during a job set a state cookie")
	}
	s.logins.mu.Lock()
	n := len(s.logins.entries)
	s.logins.mu.Unlock()
	if n != 0 {
		t.Errorf("start during a job parked %d sign-ins", n)
	}
	close(f.gate)
	waitIdle(t, s.queue, "octocat")
}

func TestOAuthCallbackWithoutCodeKeepsOptions(t *testing.T) {
	g := newFakeGitHub(t)
	s := newOAuthTestServer(t, &fakeFetcher{}, g)
	h := s.Handler()
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}, "tz": {"Asia/Saigon"}})
	rec := callback(h, url.Values{"state": {q.Get("state")}}, c)
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, "Asia/Saigon") || !strings.Contains(body, `value="octocat"`) {
		t.Fatalf("callback without code = %d: %s", rec.Code, body)
	}
	if g.exchangeCount() != 0 || s.queue.Status("octocat").State != stateNone {
		t.Error("a callback without a code exchanged or queued")
	}
}

func TestOAuthExchangeFailureKeepsOptions(t *testing.T) {
	for _, tc := range []struct{ name, fail, code string }{
		{"wrong code", "", "wrong-code"},
		{"server error", "status", testCode},
		{"not JSON", "garbage", testCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.fail = tc.fail
			s := newOAuthTestServer(t, &fakeFetcher{}, g)
			h := s.Handler()
			q, c := signIn(t, h, g, url.Values{"user": {"octocat"}, "tz": {"Asia/Saigon"}, "commits_per_repo": {"123"}})
			query := url.Values{"code": {tc.code}, "state": {q.Get("state")}}
			rec := callback(h, query, c)
			body := rec.Body.String()
			if rec.Code != http.StatusBadGateway || !strings.Contains(body, errExchange.Error()[1:]) {
				t.Fatalf("failed exchange = %d: %s", rec.Code, body)
			}
			if !strings.Contains(body, `value="octocat"`) || !strings.Contains(body, "Asia/Saigon") || !strings.Contains(body, `value="123"`) {
				t.Error("options were not kept")
			}
			if g.exchangeCount() != 1 || s.queue.Status("octocat").State != stateNone {
				t.Error("a failed exchange queued a job")
			}
			if len(g.revokedTokens()) != 0 {
				t.Error("a failed exchange revoked a token")
			}
			// The sign-in was consumed: replaying the state is refused.
			if rec := callback(h, query, c); rec.Code != http.StatusBadRequest || g.exchangeCount() != 1 {
				t.Errorf("replayed state = %d after %d exchanges", rec.Code, g.exchangeCount())
			}
		})
	}
}

func TestOAuthAccessDeniedKeepsOptions(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}, "tz": {"Asia/Saigon"}, "commits_per_repo": {"123"}})
	rec := callback(h, url.Values{"error": {"access_denied"}, "state": {q.Get("state")}}, c)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "cancelled") {
		t.Fatalf("access denied = %d: %s", rec.Code, body)
	}
	if !strings.Contains(body, `value="octocat"`) || !strings.Contains(body, "Asia/Saigon") || !strings.Contains(body, `value="123"`) {
		t.Error("options were not kept")
	}
	if g.exchangeCount() != 0 || s.queue.Status("octocat").State != stateNone {
		t.Error("a cancelled sign-in exchanged a code or queued a job")
	}
}

func TestOAuthScopeDowngrade(t *testing.T) {
	g := newFakeGitHub(t)
	g.scope = "read:user"
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{oauthToken: {Login: "octocat"}}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()
	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}, "include_private": {"1"}, "include_org_repos": {"1"}})
	if q.Get("scope") != "repo read:user read:org" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	rec := callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
	if loc := rec.Header().Get("Location"); loc != "/u/octocat?notice=granted-private" {
		t.Fatalf("callback redirected to %q", loc)
	}
	waitIdle(t, s.queue, "octocat")
	if o := f.lastCall().cfg.Options; o.IncludePrivate || o.IncludeOrgRepos {
		t.Errorf("job kept ungranted scope: %+v", o)
	}
	if m, err := s.store.Meta("octocat"); err != nil || m.Scope != "public" {
		t.Errorf("meta = %+v, %v", m, err)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/u/octocat?notice=granted-private", nil))
	if !strings.Contains(page.Body.String(), "did not grant access to private repositories") {
		t.Error("user page does not explain the downgrade")
	}
}

func TestOAuthRevokesFailedAndForeignJobs(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{oauthToken: {Login: "alice"}}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	// Signed in as alice for bob: public data only, then revoked.
	q, c := signIn(t, h, g, url.Values{"user": {"bob"}, "include_private": {"1"}})
	callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
	if st := waitIdle(t, s.queue, "bob"); st.State != stateDone {
		t.Fatalf("foreign job = %+v", st)
	}
	if o := f.lastCall().cfg.Options; o.IncludePrivate {
		t.Errorf("foreign sign-in kept private scope: %+v", o)
	}

	// A failing job revokes too.
	f.mu.Lock()
	f.err = errFresh
	f.mu.Unlock()
	q, c = signIn(t, h, g, url.Values{"user": {"alice"}})
	callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
	if st := waitIdle(t, s.queue, "alice"); st.State != stateFailed {
		t.Fatalf("failing job = %+v", st)
	}
	if got := g.revokedTokens(); len(got) != 2 {
		t.Errorf("revoked %d tokens, want 2", len(got))
	}
}

func TestOAuthRevokesWhenNotQueued(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{gate: make(chan struct{})}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	q, c := signIn(t, h, g, url.Values{"user": {"octocat"}})
	// A job starts while the user is on GitHub.
	if _, err := s.queue.Submit(signedIn(t, testToken, "user", "octocat")); err != nil {
		t.Fatal(err)
	}
	rec := callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
	if loc := rec.Header().Get("Location"); loc != "/u/octocat?notice=pending" {
		t.Errorf("callback redirected to %q", loc)
	}
	if got := g.revokedTokens(); len(got) != 1 || got[0] != oauthToken {
		t.Errorf("unused token not revoked: %q", got)
	}
	close(f.gate)
	waitIdle(t, s.queue, "octocat")
}

func TestOAuthRevokesOnShutdown(t *testing.T) {
	g := newFakeGitHub(t)
	f := &fakeFetcher{gate: make(chan struct{})}
	s := newOAuthTestServer(t, f, g)
	for _, login := range []string{"a1", "a2", "a3"} {
		if _, err := s.queue.Submit(submission{Login: login, Token: oauthToken}); err != nil {
			t.Fatal(err)
		}
	}
	s.queue.Stop() // two running, one still queued
	if got := g.revokedTokens(); len(got) != 3 {
		t.Errorf("revoked %d tokens on shutdown, want 3", len(got))
	}
}

func TestExtraScopes(t *testing.T) {
	cases := []struct {
		asked   Options
		granted string
		want    []string
	}{
		{Options{}, "read:user", nil},
		{Options{}, "repo,read:user", []string{"repo"}},
		{Options{IncludeOrgRepos: true}, "read:org,read:user", []string{"read:org"}},
		{Options{IncludePrivate: true}, "read:user,repo", nil},
		{Options{IncludePrivate: true}, "read:org,read:user,repo", []string{"read:org"}},
		{Options{IncludePrivate: true, IncludeOrgRepos: true}, "admin:org,repo", []string{"admin:org"}},
	}
	for _, c := range cases {
		if got := extraScopes(c.asked, c.granted); !slices.Equal(got, c.want) {
			t.Errorf("extraScopes(%+v, %q) = %q, want %q", c.asked, c.granted, got, c.want)
		}
	}
}

func TestOAuthWiderGrantIsRefused(t *testing.T) {
	g := newFakeGitHub(t)
	g.scope = "read:user,repo" // granted to the app by an earlier private sign-in
	f := &fakeFetcher{tokens: map[string]github.TokenInfo{oauthToken: {Login: "alice", CanReadPrivate: true}}}
	s := newOAuthTestServer(t, f, g)
	h := s.Handler()

	for _, login := range []string{"alice", "bob"} {
		q, c := signIn(t, h, g, url.Values{"user": {login}, "tz": {"Asia/Saigon"}})
		if q.Get("scope") != "read:user" {
			t.Fatalf("scope = %q", q.Get("scope"))
		}
		rec := callback(h, url.Values{"code": {testCode}, "state": {q.Get("state")}}, c)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "more access than your ticks ask for (repo)") {
			t.Fatalf("%s: wider grant = %d: %s", login, rec.Code, body)
		}
		if !strings.Contains(body, `value="`+login+`"`) || !strings.Contains(body, "Asia/Saigon") {
			t.Errorf("%s: options were not kept", login)
		}
		if s.queue.Status(login).State != stateNone {
			t.Errorf("%s: a job was queued with a token wider than the ticks", login)
		}
	}
	if got := g.revokedTokens(); len(got) != 2 {
		t.Errorf("revoked %d tokens, want 2", len(got))
	}
}

func TestOAuthPlainHTTPCookie(t *testing.T) {
	app, err := newOAuthApp(OAuthConfig{ClientID: testClientID, ClientSecret: testClientSecret, PublicURL: "http://localhost:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if app.secureCookie || app.cookieName != oauthCookieInsecure {
		t.Errorf("plain-http cookie = %q secure=%v", app.cookieName, app.secureCookie)
	}
}
