// Package github fetches profile data from the GitHub GraphQL API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const endpoint = "https://api.github.com/graphql"

// Client issues authenticated GraphQL requests.
type Client struct {
	token string
	http  *http.Client
	// warnf receives non-fatal notices (rate-limit sleeps, API ceilings).
	// Nil discards them; Collect points it at CollectConfig.Warnf.
	warnf func(format string, args ...any)
	// sleep waits out a rate limit; nil means a real, ctx-aware wait.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient returns a client authenticated with the given PAT. An empty token
// falls back to unauthenticated requests (60/h rate limit, no private data).
func NewClient(token string) *Client {
	return &Client{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// warn reports a non-fatal notice through warnf, if one is set.
func (c *Client) warn(format string, args ...any) {
	if c.warnf != nil {
		c.warnf(format, args...)
	}
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message string   `json:"message"`
	Type    string   `json:"type,omitempty"`
	Path    []string `json:"path,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors,omitempty"`
}

// maxRateLimitSleep caps how long we're willing to wait for a rate-limit
// reset before giving up — a 1-hour reset window is better handled by the
// caller (reschedule the Action) than by sleeping through it.
const maxRateLimitSleep = 5 * time.Minute

// query runs a GraphQL query and unmarshals the `data` field into out.
// Respects ctx deadlines so pagination loops can abort early when the
// caller's overall budget expires. On a rate limit (HTTP 429, a 403 primary
// or secondary limit, or a GraphQL RATE_LIMITED error) it waits once for
// Retry-After / X-RateLimit-Reset and retries, unless that wait exceeds
// maxRateLimitSleep, in which case it fails at once.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	_, err := c.queryHeader(ctx, q, vars, out)
	return err
}

// queryHeader is query that also returns the successful response's headers.
func (c *Client) queryHeader(ctx context.Context, q string, vars map[string]any, out any) (http.Header, error) {
	body, err := json.Marshal(gqlRequest{Query: q, Variables: vars})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "ghglance")
		if c.token != "" {
			req.Header.Set("Authorization", "bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("http: %w", err)
		}

		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close() // fully read already; a close error loses nothing
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}

		if rateLimited(resp, raw) && attempt == 0 {
			if err := c.waitRateLimit(ctx, resp, fmt.Sprintf("http %d", resp.StatusCode)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(raw, 500))
		}

		var r gqlResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("decode body: %w", err)
		}
		if len(r.Errors) > 0 {
			if graphQLRateLimited(r.Errors) && attempt == 0 {
				if err := c.waitRateLimit(ctx, resp, "graphql"); err != nil {
					return nil, err
				}
				continue
			}
			msgs := make([]string, 0, len(r.Errors))
			for _, e := range r.Errors {
				msgs = append(msgs, e.Message)
			}
			return nil, fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
		}
		if out != nil {
			if err := json.Unmarshal(r.Data, out); err != nil {
				return nil, fmt.Errorf("decode data: %w", err)
			}
		}
		return resp.Header, nil
	}
	return nil, fmt.Errorf("http: exceeded retry attempts")
}

// waitRateLimit sleeps until the rate limit signalled by resp should have
// lifted, or fails at once when that is further away than maxRateLimitSleep.
// source prefixes the error so the message says which signal tripped.
func (c *Client) waitRateLimit(ctx context.Context, resp *http.Response, source string) error {
	wait := rateLimitWait(resp)
	if wait > maxRateLimitSleep {
		return fmt.Errorf("%s: rate limit resets in %s (>%s max wait)", source, wait.Round(time.Second), maxRateLimitSleep)
	}
	c.warn("rate-limited, sleeping %s before retry", wait.Round(time.Second))
	sleep := c.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	return sleep(ctx, wait)
}

// sleepCtx waits for d or until ctx ends, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rateLimited reports whether an HTTP response is a GitHub rate-limit hit:
// a 429, a primary-limit 403 (X-RateLimit-Remaining is 0), or a
// secondary-limit 403, which keeps Remaining above 0 but carries Retry-After
// or says "secondary rate limit" in its body.
func rateLimited(resp *http.Response, body []byte) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return resp.Header.Get("X-RateLimit-Remaining") == "0" ||
			resp.Header.Get("Retry-After") != "" ||
			bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit"))
	}
	return false
}

// graphQLRateLimited reports whether a GraphQL error list includes GitHub's
// RATE_LIMITED type, which the API returns inside an HTTP 200 once the point
// budget runs out.
func graphQLRateLimited(errs []gqlError) bool {
	for _, e := range errs {
		if e.Type == "RATE_LIMITED" {
			return true
		}
	}
	return false
}

// rateLimitWait derives a sleep duration from response headers, in GitHub's
// documented order: Retry-After first; X-RateLimit-Reset only when
// X-RateLimit-Remaining is 0, because GitHub sends Reset on every response and
// for a secondary limit it points at the end of the unrelated hourly window;
// otherwise a 60s floor. The result is not capped, so the caller can refuse a
// reset that is too far away instead of sleeping part of the way and retrying
// while still limited.
func rateLimitWait(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			wait := time.Until(time.Unix(ts, 0))
			if wait > 0 {
				return wait + time.Second // +1s buffer
			}
		}
	}
	return 60 * time.Second
}

// truncate shortens b to at most n bytes, backing up to the last valid UTF-8
// rune boundary so the result is always well-formed.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + "…"
}
