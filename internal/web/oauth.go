package web

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub's OAuth endpoints. Tests point OAuthConfig at an httptest server.
const (
	defaultOAuthWebURL = "https://github.com"
	defaultOAuthAPIURL = "https://api.github.com"
)

const (
	// loginTTL is how long a started sign-in waits for GitHub's callback;
	// GitHub's own authorization codes expire after ten minutes too.
	loginTTL = 10 * time.Minute
	// maxPendingLogins bounds the memory held by sign-ins waiting for a
	// callback. Past it the earliest-expiring sign-in is evicted, never a
	// new one refused, so a flood of starts cannot lock real visitors out:
	// a real sign-in only has to outlive the next maxPendingLogins starts.
	maxPendingLogins = 1000
	// maxPendingPerClient bounds the sign-ins one client key holds at once.
	// A further start is refused rather than evicting one, because a client
	// key is an address that browsers behind one NAT share; a browser that
	// clicks sign-in again replaces its own earlier sign-in instead.
	maxPendingPerClient = 3
	// oauthCookie binds the browser that started a sign-in to its state.
	// Over https it carries the __Host- prefix, so a sibling subdomain
	// cannot plant one; a plain-http public URL cannot use the prefix and
	// gets the weaker unprefixed name.
	oauthCookie         = "__Host-ghglance_oauth"
	oauthCookieInsecure = "ghglance_oauth"
	// oauthTimeout bounds one call to GitHub's token or revoke endpoint.
	oauthTimeout = 15 * time.Second
)

// OAuthConfig configures "Sign in with GitHub" through a GitHub OAuth App.
// ClientID, ClientSecret and PublicURL are all required.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	// PublicURL is the site's external origin; the callback GitHub
	// redirects to is PublicURL + "/auth/callback" and must match the one
	// registered on the OAuth App exactly.
	PublicURL string
	// WebURL and APIURL override github.com and api.github.com.
	WebURL string
	APIURL string
}

// missing names the required values that are empty, in the order client
// ID, client secret, public URL.
func (c OAuthConfig) missing() []string {
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, "client ID")
	}
	if c.ClientSecret == "" {
		missing = append(missing, "client secret")
	}
	if c.PublicURL == "" {
		missing = append(missing, "public URL")
	}
	return missing
}

// oauthApp talks to GitHub on behalf of the OAuth App. It never logs a
// token, a code or the client secret.
type oauthApp struct {
	clientID     string
	clientSecret string
	redirectURI  string
	secureCookie bool
	cookieName   string
	webURL       string
	apiURL       string
	client       *http.Client
}

func newOAuthApp(c OAuthConfig) (*oauthApp, error) {
	if missing := c.missing(); len(missing) > 0 {
		return nil, fmt.Errorf("sign in with GitHub is required but not configured: missing OAuth %s", strings.Join(missing, ", "))
	}
	pub, err := url.Parse(strings.TrimRight(c.PublicURL, "/"))
	if err != nil || (pub.Scheme != "http" && pub.Scheme != "https") || pub.Host == "" || pub.RawQuery != "" || pub.Fragment != "" {
		return nil, fmt.Errorf("public URL %q must be an absolute http(s) URL such as https://ghglance.example.com", c.PublicURL)
	}
	app := &oauthApp{
		clientID:     c.ClientID,
		clientSecret: c.ClientSecret,
		redirectURI:  pub.String() + "/auth/callback",
		secureCookie: pub.Scheme == "https",
		cookieName:   oauthCookieInsecure,
		webURL:       strings.TrimRight(cmp.Or(c.WebURL, defaultOAuthWebURL), "/"),
		apiURL:       strings.TrimRight(cmp.Or(c.APIURL, defaultOAuthAPIURL), "/"),
		client:       &http.Client{Timeout: oauthTimeout},
	}
	if app.secureCookie {
		app.cookieName = oauthCookie
	}
	return app, nil
}

// webOrigin is the scheme and host of the authorize page, which the page
// CSP must allow as a form-submission redirect target.
func (a *oauthApp) webOrigin() string {
	u, err := url.Parse(a.webURL)
	if err != nil {
		return defaultOAuthWebURL
	}
	return u.Scheme + "://" + u.Host
}

// oauthScopes derives the scopes a sign-in asks for from the ticked
// options, never more: public data needs only read:user, private repos
// need repo (GitHub has no read-only private scope), and org repos add
// read:org on top of private access.
func oauthScopes(o Options) string {
	if !o.IncludePrivate {
		return "read:user"
	}
	if o.IncludeOrgRepos {
		return "repo read:user read:org"
	}
	return "repo read:user"
}

// scopeSet splits GitHub's comma- or space-separated scope list.
func scopeSet(scopes string) map[string]bool {
	has := map[string]bool{}
	for _, s := range strings.FieldsFunc(scopes, func(r rune) bool { return r == ',' || r == ' ' }) {
		has[s] = true
	}
	return has
}

// grantScopes narrows o to the scopes GitHub actually granted, which the
// user may have reduced on the consent screen. It reports which option was
// dropped ("" when none): "private" or "org".
func grantScopes(o Options, granted string) (Options, string) {
	has := scopeSet(granted)
	if o.IncludePrivate && !has["repo"] {
		o.IncludePrivate, o.IncludeOrgRepos = false, false
		return o, "private"
	}
	if o.IncludePrivate && o.IncludeOrgRepos && !has["read:org"] && !has["write:org"] && !has["admin:org"] {
		o.IncludeOrgRepos = false
		return o, "org"
	}
	return o, ""
}

// extraScopes lists the granted scopes the ticked options did not ask
// for. GitHub folds every scope a user ever granted the app into each new
// token and skips the consent screen, so a public-only sign-in can come
// back with repo from an earlier private one. A job never runs with a
// token wider than the ticks.
func extraScopes(o Options, granted string) []string {
	asked := scopeSet(oauthScopes(o))
	var extra []string
	for s := range scopeSet(granted) {
		if !asked[s] {
			extra = append(extra, s)
		}
	}
	slices.Sort(extra)
	return extra
}

// authorizeURL is GitHub's consent page for one sign-in.
func (a *oauthApp) authorizeURL(state, challenge, scope, login string) string {
	q := url.Values{
		"client_id":             {a.clientID},
		"redirect_uri":          {a.redirectURI},
		"scope":                 {scope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"login":                 {login},
	}
	return a.webURL + "/login/oauth/authorize?" + q.Encode()
}

var errExchange = errors.New("GitHub sign-in failed, try again")

// errTooManyLogins refuses a sign-in start from a client key that already
// has maxPendingPerClient sign-ins waiting for GitHub.
var errTooManyLogins = errors.New("too many sign-ins are waiting for GitHub from your address; finish one or try again in a few minutes")

// exchange trades an authorization code and its PKCE verifier for a token
// and the scopes GitHub granted.
func (a *oauthApp) exchange(ctx context.Context, code, verifier string) (token, scope string, err error) {
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	form := url.Values{
		"client_id":     {a.clientID},
		"client_secret": {a.clientSecret},
		"code":          {code},
		"redirect_uri":  {a.redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.webURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("oauth: token exchange failed: %v", err)
		return "", "", errExchange
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil || resp.StatusCode != http.StatusOK {
		log.Printf("oauth: token exchange failed: status %d", resp.StatusCode)
		return "", "", errExchange
	}
	if body.Error != "" || !tokenRE.MatchString(body.AccessToken) {
		log.Printf("oauth: token exchange refused: %s", strconv.Quote(body.Error))
		return "", "", errExchange
	}
	return body.AccessToken, body.Scope, nil
}

// revoke deletes the grant's token on GitHub. It is best effort: a token
// that cannot be revoked still never leaves the server, and the user can
// revoke it under Settings > Applications.
func (a *oauthApp) revoke(token string) {
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), oauthTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		a.apiURL+"/applications/"+url.PathEscape(a.clientID)+"/token", bytes.NewReader(body))
	if err != nil {
		log.Printf("oauth: revoke failed: %v", err)
		return
	}
	req.SetBasicAuth(a.clientID, a.clientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("oauth: revoke failed: %v", err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		log.Printf("oauth: revoke returned status %d", resp.StatusCode)
	}
}

// pendingLogin is a submission waiting for GitHub's callback.
type pendingLogin struct {
	client   string // clientKey of the browser that started it
	sub      submission
	form     formValues
	verifier string
	expires  time.Time
}

// pendingLogins holds started sign-ins by state, in memory only.
type pendingLogins struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*pendingLogin
}

func newPendingLogins() *pendingLogins {
	return &pendingLogins{now: time.Now, entries: map[string]*pendingLogin{}}
}

// add stores p under a fresh random state and returns the state. replaces
// is the state in the starting browser's cookie, if any: that browser's
// earlier sign-in, which the new cookie supersedes, so it is dropped first.
// add drops expired sign-ins, refuses p with errTooManyLogins when p.client
// already holds maxPendingPerClient, and otherwise makes room by evicting
// the earliest-expiring of all when the table is at maxPendingLogins.
func (l *pendingLogins) add(p *pendingLogin, replaces string) (string, error) {
	state, err := randomString()
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if replaces != "" {
		delete(l.entries, replaces)
	}
	now := l.now()
	var oldest string
	var oldestAt time.Time
	own := 0
	for k, e := range l.entries {
		if !now.Before(e.expires) {
			delete(l.entries, k)
			continue
		}
		if oldest == "" || e.expires.Before(oldestAt) {
			oldest, oldestAt = k, e.expires
		}
		if e.client == p.client {
			own++
		}
	}
	if own >= maxPendingPerClient {
		return "", errTooManyLogins
	}
	if len(l.entries) >= maxPendingLogins {
		delete(l.entries, oldest)
	}
	p.expires = now.Add(loginTTL)
	l.entries[state] = p
	return state, nil
}

// take removes and returns the sign-in for state, or nil when it is
// unknown, already used or expired. A state is good for one callback.
func (l *pendingLogins) take(state string) *pendingLogin {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.entries[state]
	if !ok {
		return nil
	}
	delete(l.entries, state)
	if !l.now().Before(p.expires) {
		return nil
	}
	return p
}

// randomString is 256 random bits, base64url-encoded: 43 characters, which
// also fits PKCE's 43–128 character verifier.
func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge is the S256 code challenge for verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// handleAuthStart validates the generation form, parks it under a random
// state and sends the browser to GitHub's consent page. A pasted token
// takes precedence over signing in, whichever button sent the form (Enter
// in the token field presses the first one, the sign-in button): the job
// is queued on that token straight away, under the same ownership,
// privacy and cooldown rules, and the token is never revoked.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	sub, form, ok := s.readSubmission(w, r)
	if !ok {
		return
	}
	if sub.Pasted {
		w.Header().Set("Cache-Control", "no-store")
		s.enqueue(w, r, sub, form, "")
		return
	}
	if s.queue.Status(sub.Login).Active() {
		http.Redirect(w, r, "/u/"+url.PathEscape(userKey(sub.Login))+"?notice=pending", http.StatusSeeOther)
		return
	}
	verifier, err := randomString()
	if err != nil {
		s.render(w, http.StatusInternalServerError, "index", pageData{Title: "ghglance", Error: capitalize(errExchange.Error()) + ".", Form: form})
		return
	}
	var replaces string
	if c, err := r.Cookie(s.oauth.cookieName); err == nil {
		replaces = c.Value
	}
	state, err := s.logins.add(&pendingLogin{client: clientKey(r), sub: sub, form: form, verifier: verifier}, replaces)
	if errors.Is(err, errTooManyLogins) {
		s.render(w, http.StatusTooManyRequests, "index", pageData{Title: "ghglance", Error: capitalize(err.Error()) + ".", Form: form})
		return
	}
	if err != nil {
		s.render(w, http.StatusInternalServerError, "index", pageData{Title: "ghglance", Error: capitalize(errExchange.Error()) + ".", Form: form})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.oauth.cookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   int(loginTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.oauth.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.oauth.authorizeURL(state, pkceChallenge(verifier), oauthScopes(sub.Options), sub.Login), http.StatusFound)
}

// handleAuthCallback finishes a sign-in: it checks the state against the
// browser's cookie, trades the code for a token and queues the job, which
// enforces the ownership, privacy and cooldown rules once it knows whose
// token it holds. The token is revoked when the job ends.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{
		Name: s.oauth.cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.oauth.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	q := r.URL.Query()
	state := q.Get("state")
	c, err := r.Cookie(s.oauth.cookieName)
	if state == "" || err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		s.renderMessage(w, http.StatusBadRequest, "Sign-in failed",
			"This sign-in was not started in this browser, or it was already used. Start again from the form.")
		return
	}
	p := s.logins.take(state)
	if p == nil {
		s.renderMessage(w, http.StatusBadRequest, "Sign-in expired",
			"This sign-in expired or was already used. Start again from the form.")
		return
	}

	if e := q.Get("error"); e != "" {
		msg := "GitHub sign-in failed. Your options are kept below; try again."
		if e == "access_denied" {
			msg = "GitHub sign-in was cancelled, so nothing was generated. Your options are kept below."
		} else {
			log.Printf("oauth: callback error %s", strconv.Quote(e))
		}
		s.render(w, http.StatusOK, "index", pageData{Title: "ghglance", Error: msg, Form: p.form})
		return
	}
	code := q.Get("code")
	if code == "" {
		s.render(w, http.StatusBadRequest, "index", pageData{Title: "ghglance", Error: capitalize(errExchange.Error()) + ".", Form: p.form})
		return
	}
	token, granted, err := s.oauth.exchange(r.Context(), code, p.verifier)
	if err != nil {
		s.render(w, http.StatusBadGateway, "index", pageData{Title: "ghglance", Error: capitalize(err.Error()) + ".", Form: p.form})
		return
	}

	if extra := extraScopes(p.sub.Options, granted); len(extra) > 0 {
		s.oauth.revoke(token)
		s.render(w, http.StatusOK, "index", pageData{Title: "ghglance", Form: p.form, Error: fmt.Sprintf(
			"GitHub returned a token with more access than your ticks ask for (%s), because you granted it to ghglance before. "+
				"Nothing was generated and the token was revoked. Tick the matching options, or revoke ghglance under GitHub "+
				"Settings > Applications > Authorized OAuth Apps and sign in again.", strings.Join(extra, ", "))})
		return
	}

	sub := p.sub
	sub.Token = token
	var dropped string
	sub.Options, dropped = grantScopes(sub.Options, granted)
	notice := ""
	if dropped != "" {
		notice = "granted-" + dropped
	}
	if !s.enqueue(w, r, sub, p.form, notice) {
		s.oauth.revoke(token)
	}
}
