package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/tiennm99/ghglance/internal/github"
)

// Job states reported by the status endpoint.
const (
	stateQueued  = "queued"
	stateRunning = "running"
	stateDone    = "done"
	stateFailed  = "failed"
	stateNone    = "none"
)

// finishedJobTTL is how long a finished job's status stays queryable, long
// enough for a polling page to see "done" or the failure reason.
const finishedJobTTL = time.Hour

// queueCapacity bounds jobs waiting for a worker; submissions beyond it are
// refused rather than piling up in memory.
const queueCapacity = 64

// maxJobsPerSubmitter bounds the queued or running jobs one GitHub account
// may have at once, across sign-ins and pasted tokens, so one submitter
// cannot fill the queue.
const maxJobsPerSubmitter = 3

// tokenCheckTimeout bounds the submit-time lookup of whose token a
// submission carries.
const tokenCheckTimeout = 15 * time.Second

var (
	errQueueFull = errors.New("the generation queue is full, try again in a few minutes")
	errStopped   = errors.New("server is shutting down")
	errNoToken   = errors.New("this generation has no token; sign in with GitHub or paste your own token")
	errFresh     = errors.New("these cards are recent; you signed in as another account, so it does not skip the wait")
	// errFreshPasted is errFresh for a pasted token.
	errFreshPasted = errors.New("these cards are recent; your token belongs to another account, so it does not skip the wait")
	errTooManyJobs = fmt.Errorf("you already have %d card generations queued or running; wait for one to finish", maxJobsPerSubmitter)
	// errForeignPrivate refuses a private-capable sign-in for another
	// account; errForeignPrivatePasted is the same for a pasted token.
	// Neither names the token's account: the message is shown on the
	// target's public page.
	errForeignPrivate       = errors.New("you signed in with access to private repositories, so you can only generate cards for your own account")
	errForeignPrivatePasted = errors.New("the token can read private repositories, so it can only generate cards for its own account")
	// errOwnPrivateUnticked refuses an own token that can read private
	// repos while private repos are unticked: GitHub would fold private
	// contributions into cards labelled as public data.
	errOwnPrivateUnticked       = errors.New("your sign-in can read private repositories: tick Include private repos and sign in again")
	errOwnPrivateUntickedPasted = errors.New("your token can read private repositories: tick Include private repos, or paste a token with only the read:user scope")
)

// Fetcher runs the GitHub fetch. Tests swap in a fake; the server uses
// github.Collect.
type Fetcher interface {
	Fetch(ctx context.Context, token, login string, cfg github.CollectConfig) (*github.Profile, error)
	TokenInfo(ctx context.Context, token string) (github.TokenInfo, error)
}

type githubFetcher struct{}

func (githubFetcher) Fetch(ctx context.Context, token, login string, cfg github.CollectConfig) (*github.Profile, error) {
	return github.Collect(ctx, github.NewClient(token), login, cfg)
}

func (githubFetcher) TokenInfo(ctx context.Context, token string) (github.TokenInfo, error) {
	return github.NewClient(token).TokenInfo(ctx)
}

// job is one queued generation. token is the submitter's token, held only
// until the job starts and never logged or persisted; the worker running
// the job holds it after that. A sign-in token is revoked on GitHub when
// the job ends; a pasted one (pasted is true) belongs to the visitor and is
// only dropped. info describes the token, looked up at submit time;
// submitter is its lowercased owner, empty without a token.
type job struct {
	key       string
	login     string
	opts      Options
	token     string
	pasted    bool
	info      github.TokenInfo
	submitter string

	state    string
	stage    string
	err      string
	queued   time.Time
	started  time.Time
	finished time.Time
}

// JobStatus is the polling view of a job.
type JobStatus struct {
	State    string `json:"state"`
	Stage    string `json:"stage,omitempty"`
	Error    string `json:"error,omitempty"`
	Position int    `json:"position,omitempty"`
	Elapsed  int    `json:"elapsed_seconds,omitempty"`
}

func (s JobStatus) Active() bool { return s.State == stateQueued || s.State == stateRunning }

// Queue runs generation jobs on a fixed worker pool, with at most one queued
// or running job per user and maxJobsPerSubmitter per token owner.
type Queue struct {
	store    *Store
	fetcher  Fetcher
	timeout  time.Duration
	cooldown time.Duration
	now      func() time.Time
	// revoke deletes a sign-in token on GitHub once its job is over. It is
	// never called with a pasted token.
	revoke func(token string)

	mu      sync.Mutex
	jobs    map[string]*job
	pending []*job
	wake    chan struct{}
	closed  bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newQueue(store *Store, fetcher Fetcher, revoke func(token string), timeout, cooldown time.Duration, workers int) *Queue {
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		store:    store,
		fetcher:  fetcher,
		timeout:  timeout,
		cooldown: cooldown,
		now:      time.Now,
		revoke:   revoke,
		jobs:     map[string]*job{},
		wake:     make(chan struct{}, queueCapacity),
		ctx:      ctx,
		cancel:   cancel,
	}
	for range workers {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Submit queues a job for sub.Login. When that user already has a queued or
// running job, it is left alone and created is false. It asks GitHub whose
// token sub carries, refuses a token GitHub rejects, and refuses a
// submission whose token owner already has maxJobsPerSubmitter jobs queued
// or running.
func (q *Queue) Submit(sub submission) (created bool, err error) {
	key := userKey(sub.Login)
	// A cheap first pass spares the GitHub lookup for a submission that
	// would be deduplicated or refused anyway.
	q.mu.Lock()
	dup, err := q.admitLocked(key, "")
	q.mu.Unlock()
	if dup || err != nil {
		return false, err
	}

	var info github.TokenInfo
	if sub.Token != "" {
		ctx, cancel := context.WithTimeout(q.ctx, tokenCheckTimeout)
		info, err = q.fetcher.TokenInfo(ctx, sub.Token)
		cancel()
		if err != nil {
			if q.ctx.Err() != nil {
				return false, errStopped
			}
			if sub.Pasted {
				return false, errors.New("GitHub did not accept your token")
			}
			return false, errors.New("GitHub did not accept your sign-in token")
		}
	}
	submitter := strings.ToLower(info.Login)

	q.mu.Lock()
	defer q.mu.Unlock()
	if dup, err := q.admitLocked(key, submitter); dup || err != nil {
		return false, err
	}
	j := &job{
		key:       key,
		login:     sub.Login,
		opts:      sub.Options,
		token:     sub.Token,
		pasted:    sub.Pasted,
		info:      info,
		submitter: submitter,
		state:     stateQueued,
		queued:    q.now(),
	}
	q.jobs[key] = j
	q.pending = append(q.pending, j)
	q.wake <- struct{}{}
	return true, nil
}

// admitLocked checks whether a job for key may be queued. dup is true when
// key already has an active job. A non-empty submitter is also refused once
// it has maxJobsPerSubmitter active jobs.
func (q *Queue) admitLocked(key, submitter string) (dup bool, err error) {
	if q.closed {
		return false, errStopped
	}
	q.pruneLocked()
	if j, ok := q.jobs[key]; ok && j.active() {
		return true, nil
	}
	if len(q.pending) >= queueCapacity {
		return false, errQueueFull
	}
	if submitter != "" {
		n := 0
		for _, j := range q.jobs {
			if j.submitter == submitter && j.active() {
				n++
			}
		}
		if n >= maxJobsPerSubmitter {
			return false, errTooManyJobs
		}
	}
	return false, nil
}

func (j *job) active() bool { return j.state == stateQueued || j.state == stateRunning }

// Status reports the job for login, or stateNone when there is none.
func (q *Queue) Status(login string) JobStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[userKey(login)]
	if !ok {
		return JobStatus{State: stateNone}
	}
	st := JobStatus{State: j.state, Stage: j.stage, Error: j.err}
	switch j.state {
	case stateQueued:
		for i, p := range q.pending {
			if p == j {
				st.Position = i + 1
				break
			}
		}
		st.Elapsed = int(q.now().Sub(j.queued).Seconds())
	case stateRunning:
		st.Elapsed = int(q.now().Sub(j.started).Seconds())
	}
	return st
}

// Stop cancels running jobs, drops queued ones and waits for the workers.
// Sign-in tokens of dropped jobs are revoked before it returns; running
// jobs revoke theirs as their workers wind down. Pasted tokens are only
// dropped.
func (q *Queue) Stop() {
	q.mu.Lock()
	q.closed = true
	var dropped []string
	for _, j := range q.pending {
		if !j.pasted {
			dropped = append(dropped, j.token)
		}
		j.token = ""
		j.state, j.err = stateFailed, errStopped.Error()
	}
	q.pending = nil
	q.mu.Unlock()
	q.cancel()
	var revokes sync.WaitGroup
	for _, token := range dropped {
		revokes.Go(func() { q.revokeToken(token) })
	}
	revokes.Wait()
	q.wg.Wait()
}

// revokeToken revokes a sign-in token; an empty one has nothing to revoke.
func (q *Queue) revokeToken(token string) {
	if q.revoke != nil && token != "" {
		q.revoke(token)
	}
}

// pruneLocked forgets finished jobs older than finishedJobTTL.
func (q *Queue) pruneLocked() {
	cutoff := q.now().Add(-finishedJobTTL)
	for k, j := range q.jobs {
		if (j.state == stateDone || j.state == stateFailed) && j.finished.Before(cutoff) {
			delete(q.jobs, k)
		}
	}
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		}
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.mu.Unlock()
			continue
		}
		j := q.pending[0]
		q.pending = q.pending[1:]
		j.state, j.stage, j.started = stateRunning, "fetching from GitHub", q.now()
		// The worker holds the token from here on; the job drops its copy.
		token := j.token
		j.token = ""
		q.mu.Unlock()

		err := q.run(j, token)

		// Revoke before reporting the job over, so a finished job never
		// leaves a live sign-in token behind. A pasted token is the
		// visitor's to keep or revoke; it is only dropped.
		if !j.pasted {
			q.revokeToken(token)
		}

		q.mu.Lock()
		j.finished = q.now()
		if err != nil {
			j.state, j.stage, j.err = stateFailed, "", err.Error()
		} else {
			j.state, j.stage = stateDone, ""
		}
		q.mu.Unlock()
		if err != nil {
			log.Printf("job %s: failed after %s: %v", j.key, j.finished.Sub(j.started).Round(time.Second), err)
		} else {
			log.Printf("job %s: done in %s", j.key, j.finished.Sub(j.started).Round(time.Second))
		}
	}
}

// run fetches and publishes one job's cards under the per-job timeout.
//
// Published cards are public, so a job only renders what the anonymous
// view of GitHub would show, unless the token belongs to the target user.
// GitHub folds every private contribution a token can see into the totals
// and the calendar, so filtering repo lists alone is not enough: a token
// that can read private repos is refused for anyone but its owner, and
// for its owner unless private repos are ticked.
func (q *Queue) run(j *job, token string) error {
	ctx := q.ctx
	if q.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.timeout)
		defer cancel()
	}

	if token == "" {
		return errNoToken
	}
	opts := j.opts
	info := j.info
	own := strings.EqualFold(info.Login, j.login)
	if own && info.CanReadPrivate && !opts.IncludePrivate {
		if j.pasted {
			return errOwnPrivateUntickedPasted
		}
		return errOwnPrivateUnticked
	}
	if !own {
		if info.CanReadPrivate {
			if j.pasted {
				return errForeignPrivatePasted
			}
			return errForeignPrivate
		}
		opts.IncludePrivate, opts.IncludeOrgRepos = false, false
		if q.store.cooldownLeft(j.login, q.cooldown, q.now()) > 0 {
			if j.pasted {
				return errFreshPasted
			}
			return errFresh
		}
	}

	cfg := opts.collectConfig()
	cfg.Strict = true
	cfg.Warnf = func(format string, args ...any) {
		log.Printf("job %s: warn: "+format, append([]any{j.key}, args...)...)
	}
	profile, err := q.fetcher.Fetch(ctx, token, j.login, cfg)
	if q.ctx.Err() != nil {
		return errStopped
	}
	if err == nil {
		// A fetch that ran out of time may still hand back a profile; a
		// partial set must never replace a complete one.
		err = ctx.Err()
	}
	if err != nil {
		return publicError(err)
	}

	q.mu.Lock()
	j.stage = "rendering cards"
	q.mu.Unlock()

	scope := "public"
	if own && info.CanReadPrivate && opts.IncludePrivate {
		scope = "private"
	}
	return q.store.Publish(profile, Meta{
		Login:       j.login,
		GeneratedAt: q.now().UTC(),
		Scope:       scope,
		Options:     opts,
	})
}

// publicError trims a fetch error to something fit for the status page.
func publicError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timed out while fetching from GitHub")
	}
	msg := err.Error()
	if strings.Contains(msg, "user not found") || strings.Contains(msg, "Could not resolve to a User") {
		return errors.New("GitHub user not found")
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	return errors.New(msg)
}
