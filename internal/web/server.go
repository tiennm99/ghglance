// Package web serves the ghglance web UI: a form that queues card
// generation for any GitHub user on the visitor's own GitHub access (a
// "Sign in with GitHub" token or a token they paste), and pages that show
// the stored cards for quick viewing. Cards are inlined into the page as
// data: URIs; there is no URL to link to or embed a card.
package web

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tiennm99/ghglance/internal/card"
	"github.com/tiennm99/ghglance/internal/theme"
)

//go:embed templates static
var assets embed.FS

// maxFormBytes bounds a form post; the real form is well under 1 KiB.
const maxFormBytes = 16 << 10

// Submission rate per client IP: a burst of 5, then one every 2 minutes.
const (
	submitBurst    = 5
	submitInterval = 2 * time.Minute
)

const (
	// pageCSP takes GitHub's origin as an extra form-action source: the
	// sign-in form is redirected to GitHub's consent page, and browsers
	// check redirects of a form submission against form-action too.
	// img-src allows data: for the cards, which the user page inlines; an
	// SVG loaded through <img> runs no scripts and fetches nothing.
	pageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
		"connect-src 'self'; form-action 'self' %s; base-uri 'none'; frame-ancestors 'none'"
	// maxCardBytes bounds one stored card read into the user page; real
	// cards are a few tens of KiB at most.
	maxCardBytes = 1 << 20
)

// Config configures the web server.
type Config struct {
	Addr     string
	DataDir  string
	Cooldown time.Duration
	// Retention is how long generated cards are kept before they are
	// deleted (0 = forever).
	Retention time.Duration
	Workers   int
	// JobTimeout bounds one generation job (0 = no limit).
	JobTimeout time.Duration
	// Fetcher overrides the GitHub fetch; nil uses the real API.
	Fetcher Fetcher
	// OAuth configures "Sign in with GitHub". It is required; visitors may
	// paste their own token instead of signing in, but the server never
	// uses a token of its own.
	OAuth OAuthConfig
}

// Server holds the store, job queue and handlers.
type Server struct {
	cfg     Config
	store   *Store
	queue   *Queue
	limiter *rateLimiter
	pages   map[string]*template.Template
	static  http.Handler
	csp     string
	oauth   *oauthApp
	logins  *pendingLogins
	// callbacks tracks sign-in callbacks still in their handler. One can
	// hold a fresh token past the HTTP drain, so Close waits for it to be
	// queued or revoked.
	callbacks sync.WaitGroup
}

// New opens the data directory and starts the job workers.
func New(cfg Config) (*Server, error) {
	if cfg.Fetcher == nil {
		cfg.Fetcher = githubFetcher{}
	}
	oauth, err := newOAuthApp(cfg.OAuth)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	pages, err := parsePages()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		store:   store,
		limiter: newRateLimiter(submitBurst, submitInterval),
		pages:   pages,
		static:  http.FileServerFS(staticFS),
		csp:     fmt.Sprintf(pageCSP, oauth.webOrigin()),
		oauth:   oauth,
		logins:  newPendingLogins(),
	}
	s.queue = newQueue(store, cfg.Fetcher, oauth.revoke, cfg.JobTimeout, cfg.Cooldown, cfg.Workers)
	return s, nil
}

// Close stops the workers, waits for sign-in callbacks still holding a
// token to queue or revoke it, and releases the data directory.
func (s *Server) Close() {
	s.queue.Stop()
	s.callbacks.Wait()
	if err := s.store.Close(); err != nil {
		log.Printf("close data directory: %v", err)
	}
}

// shutdownTimeout bounds draining in-flight requests on shutdown. After it,
// Close stops the workers and revokes dropped sign-in tokens, each revoke
// call bounded by oauthTimeout, so the container's stop grace period
// (stop_grace_period in compose.yml) must cover at least shutdownTimeout
// plus two oauthTimeout windows (dropped queued jobs, then running ones).
// A sign-in callback that outlasts the drain is covered by the same budget:
// its token exchange, bounded by oauthTimeout, ends inside the first window
// after the drain, the stopped queue refuses its job at once, and its
// revoke ends inside the second.
const shutdownTimeout = 15 * time.Second

// Run serves until ctx is cancelled, then drains in-flight requests and
// stops the job workers. Queued and running jobs are abandoned; nothing
// half-rendered is ever published.
func Run(ctx context.Context, cfg Config) error {
	s, err := New(cfg)
	if err != nil {
		return err
	}
	defer s.Close()
	log.Printf("sign in with GitHub callback %s", s.oauth.redirectURI)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Printf("serving on %s, data in %s", ln.Addr(), s.store.dir)
	go s.expireLoop(ctx)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Printf("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Handler returns the routed, hardened HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /auth/start", s.handleAuthStart)
	mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	mux.HandleFunc("GET /u/{user}", s.handleUser)
	mux.HandleFunc("GET /u/{user}/status", s.handleStatus)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.withCache(s.static, "public, max-age=3600")))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderMessage(w, http.StatusNotFound, "Page not found", "There is nothing at this address.")
	})

	cop := http.NewCrossOriginProtection()
	return commonHeaders(cop.Handler(mux), s.csp)
}

func commonHeaders(next http.Handler, csp string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withCache(next http.Handler, value string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}

// pageData feeds every template.
type pageData struct {
	Title   string
	Heading string
	Message string
	Error   string
	Form    formValues

	Login        string
	Key          string
	Meta         *Meta
	Job          JobStatus
	Notice       string
	CooldownLeft string
	ExpiresIn    string
	Themes       []string
	Theme        string
	Cards        []cardView
}

// cardView is one card on the user page. Src is a data: URI of the stored
// SVG, so the page carries the card itself and no card URL.
type cardView struct {
	Name string
	Src  template.URL
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "index", pageData{Title: "ghglance", Form: defaultForm()})
}

// readSubmission parses, rate-limits and validates a generation form. On
// failure it has already written the response.
func (s *Server) readSubmission(w http.ResponseWriter, r *http.Request) (submission, formValues, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.render(w, http.StatusBadRequest, "index", pageData{Title: "ghglance", Error: "The form could not be read.", Form: defaultForm()})
		return submission{}, formValues{}, false
	}
	sub, form, err := parseSubmission(r.PostForm.Get)
	if !s.limiter.Allow(clientKey(r)) {
		w.Header().Set("Retry-After", strconv.Itoa(int(submitInterval.Seconds())))
		s.render(w, http.StatusTooManyRequests, "index", pageData{
			Title: "ghglance",
			Error: "Too many submissions from your address. Wait a couple of minutes and try again.",
			Form:  form,
		})
		return submission{}, formValues{}, false
	}
	if err != nil {
		s.render(w, http.StatusBadRequest, "index", pageData{Title: "ghglance", Error: capitalize(err.Error()) + ".", Form: form})
		return submission{}, formValues{}, false
	}
	return sub, form, true
}

// enqueue queues sub and redirects to the user's page, adding notice when
// one is given. It reports whether a new job took the submission's token;
// when not, the caller still owns it. The cooldown is checked by the job,
// once it knows whose token it holds: a token for the target account
// skips it.
func (s *Server) enqueue(w http.ResponseWriter, r *http.Request, sub submission, form formValues, notice string) bool {
	target := "/u/" + url.PathEscape(userKey(sub.Login))
	if s.queue.Status(sub.Login).Active() {
		http.Redirect(w, r, target+"?notice=pending", http.StatusSeeOther)
		return false
	}
	created, err := s.queue.Submit(sub)
	if err != nil {
		s.render(w, http.StatusServiceUnavailable, "index", pageData{Title: "ghglance", Error: capitalize(err.Error()) + ".", Form: form})
		return false
	}
	switch {
	case !created:
		target += "?notice=pending"
	case notice != "":
		target += "?notice=" + url.QueryEscape(notice)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return created
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	login := r.PathValue("user")
	if !validUsername(login) {
		s.renderMessage(w, http.StatusNotFound, "Not a GitHub username", "GitHub usernames use letters, digits and single hyphens, up to 39 characters.")
		return
	}
	meta, err := s.store.Meta(login)
	if err != nil && !isNotExist(err) {
		log.Printf("read meta %s: %v", userKey(login), err)
		s.renderMessage(w, http.StatusInternalServerError, "Something went wrong", "The stored cards could not be read.")
		return
	}
	job := s.queue.Status(login)
	if meta == nil && job.State == stateNone {
		form := defaultForm()
		form.User = login
		s.render(w, http.StatusNotFound, "index", pageData{
			Title:   "ghglance",
			Message: fmt.Sprintf("No cards for %s yet. Generate them below.", login),
			Form:    form,
		})
		return
	}

	d := pageData{
		Title:  login + " · ghglance",
		Login:  login,
		Key:    userKey(login),
		Meta:   meta,
		Job:    job,
		Themes: theme.IDs(),
		Theme:  defaultTheme,
		Form:   defaultForm(),
	}
	d.Form.User = login
	if t := r.URL.Query().Get("theme"); validTheme(t) {
		d.Theme = t
	}
	switch r.URL.Query().Get("notice") {
	case "pending":
		if job.Active() {
			d.Notice = "A generation for this user is already in progress."
		}
	case "granted-private":
		d.Notice = "GitHub did not grant access to private repositories, so this generation counts public data only."
	case "granted-org":
		d.Notice = "GitHub did not grant read:org, so this generation leaves out org repos."
	}
	if meta != nil {
		d.Login = meta.Login
		d.Form = formFromOptions(meta.Login, meta.Options)
		if left := s.store.cooldownLeft(login, s.cfg.Cooldown, time.Now()); left > 0 {
			d.CooldownLeft = humanDuration(left)
		}
		if s.cfg.Retention > 0 {
			d.ExpiresIn = humanDuration(max(time.Until(meta.GeneratedAt.Add(s.cfg.Retention)), time.Minute))
		}
		for _, f := range card.Filenames() {
			src, err := s.cardDataURI(login, d.Theme, f)
			if err != nil {
				// A set expiring or being replaced between the meta read
				// and this one is not worth logging; it just drops a card.
				if !isNotExist(err) {
					log.Printf("read card %s/%s/%s: %v", userKey(login), d.Theme, f, err)
				}
				continue
			}
			name := strings.ReplaceAll(strings.TrimSuffix(f, ".svg"), "-", " ")
			d.Cards = append(d.Cards, cardView{Name: name, Src: src})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, http.StatusOK, "user", d)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	login := r.PathValue("user")
	if !validUsername(login) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// A failed write means the poller went away; there is no one to tell.
	_ = json.NewEncoder(w).Encode(s.queue.Status(login))
}

// cardDataURI reads one stored card and returns it as a base64 data: URI.
// The bytes are our own renderer's output and base64 cannot break out of
// the attribute, so the URI is marked safe for html/template, which would
// otherwise replace any data: URL.
func (s *Server) cardDataURI(login, themeID, file string) (template.URL, error) {
	f, err := s.store.OpenCard(login, themeID, file)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fs.ErrNotExist
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxCardBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxCardBytes {
		return "", fmt.Errorf("card larger than %d bytes", maxCardBytes)
	}
	return template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(raw)), nil
}

func (s *Server) render(w http.ResponseWriter, status int, page string, d pageData) {
	var buf strings.Builder
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", d); err != nil {
		log.Printf("render %s: %v", page, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, buf.String())
}

func (s *Server) renderMessage(w http.ResponseWriter, status int, heading, message string) {
	s.render(w, status, "message", pageData{Title: heading + " · ghglance", Heading: heading, Message: message})
}

func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"fmtTime": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"index", "user", "message"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(assets,
			"templates/layout.html", "templates/form.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", name, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// formFromOptions pre-fills the regenerate form with a set's last options.
// Private and org scope are ticked only when the last generation used them,
// so a regenerate asks GitHub for no more than the set already shows.
func formFromOptions(login string, o Options) formValues {
	return formValues{
		User:            login,
		TZ:              o.TZ,
		StartOfWeek:     o.StartOfWeek,
		IncludeForks:    o.IncludeForks,
		IncludeOrgRepos: o.IncludeOrgRepos,
		IncludePrivate:  o.IncludePrivate,
		CommitsPerRepo:  strconv.Itoa(o.CommitsPerRepo),
	}
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	}
	return "under a minute"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// expireInterval is how often expired cards are swept while serving.
const expireInterval = time.Hour

// expireLoop deletes cards older than the retention once at startup and then
// every expireInterval until ctx is cancelled.
func (s *Server) expireLoop(ctx context.Context) {
	if s.cfg.Retention <= 0 {
		return
	}
	t := time.NewTicker(expireInterval)
	defer t.Stop()
	for {
		if n := s.store.Expire(s.cfg.Retention, time.Now()); n > 0 {
			log.Printf("expired cards for %d user(s) older than %s", n, s.cfg.Retention)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
