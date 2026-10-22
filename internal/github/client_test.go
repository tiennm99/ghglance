package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sequence replies with each fakeReply in turn, repeating the last one.
func sequence(replies ...fakeReply) func(fakeCall) fakeReply {
	i := 0
	return func(fakeCall) fakeReply {
		r := replies[min(i, len(replies)-1)]
		i++
		return r
	}
}

func headers(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

const okBody = `{"data":{"n":1}}`

func TestQueryRetriesOnceAfterRateLimit(t *testing.T) {
	resetSoon := strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10)
	resetLate := strconv.FormatInt(time.Now().Add(47*time.Minute).Unix(), 10)
	cases := []struct {
		name  string
		first fakeReply
		// minWait and maxWait bound the sleep taken before the retry.
		minWait, maxWait time.Duration
	}{
		{
			name:    "429 with Retry-After",
			first:   fakeReply{Status: 429, Header: headers("Retry-After", "2")},
			minWait: 2 * time.Second, maxWait: 2 * time.Second,
		},
		{
			name: "secondary-limit 403 with Retry-After and quota left",
			first: fakeReply{Status: 403, Header: headers(
				"Retry-After", "3", "X-RateLimit-Remaining", "4321")},
			minWait: 3 * time.Second, maxWait: 3 * time.Second,
		},
		{
			name: "secondary-limit 403 recognised by its body",
			first: fakeReply{Status: 403, Header: headers(
				"X-RateLimit-Remaining", "4321", "X-RateLimit-Reset", resetLate),
				Body: `{"message":"You have exceeded a secondary rate limit."}`},
			minWait: 60 * time.Second, maxWait: 60 * time.Second,
		},
		{
			name: "primary-limit 403 with a reset inside the cap",
			first: fakeReply{Status: 403, Header: headers(
				"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetSoon)},
			minWait: 20 * time.Second, maxWait: 32 * time.Second,
		},
		{
			name: "GraphQL RATE_LIMITED error inside a 200",
			first: fakeReply{Header: headers("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetSoon),
				Body: `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`},
			minWait: 20 * time.Second, maxWait: 32 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, f := newFakeClient(t, sequence(tc.first, fakeReply{Body: okBody}))
			var out struct{ N int }
			if err := c.query(context.Background(), "q", nil, &out); err != nil {
				t.Fatalf("query: %v", err)
			}
			if out.N != 1 {
				t.Errorf("decoded n = %d, want 1 from the retried response", out.N)
			}
			if len(f.calls) != 2 {
				t.Errorf("requests = %d, want 2 (one retry)", len(f.calls))
			}
			if len(f.sleeps) != 1 || f.sleeps[0] < tc.minWait || f.sleeps[0] > tc.maxWait {
				t.Errorf("sleeps = %v, want one in [%s, %s]", f.sleeps, tc.minWait, tc.maxWait)
			}
		})
	}
}

func TestQueryFailsFastWhenResetIsBeyondMaxWait(t *testing.T) {
	resetLater := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	cases := map[string]fakeReply{
		"reset an hour away": {Status: 403, Header: headers(
			"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetLater)},
		"Retry-After of an hour": {Status: 429, Header: headers("Retry-After", "3600")},
		"GraphQL RATE_LIMITED with a distant reset": {
			Header: headers("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetLater),
			Body:   `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			c, f := newFakeClient(t, sequence(reply, fakeReply{Body: okBody}))
			err := c.query(context.Background(), "q", nil, nil)
			if err == nil || !strings.Contains(err.Error(), "rate limit resets in") {
				t.Fatalf("err = %v, want the fail-fast reset error", err)
			}
			if len(f.sleeps) != 0 || len(f.calls) != 1 {
				t.Errorf("sleeps = %v, requests = %d; want no sleep and no retry", f.sleeps, len(f.calls))
			}
		})
	}
}

func TestQueryGivesUpAfterOneRetry(t *testing.T) {
	c, f := newFakeClient(t, sequence(fakeReply{Status: 429, Header: headers("Retry-After", "1")}))
	err := c.query(context.Background(), "q", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "http 429") {
		t.Fatalf("err = %v, want http 429 after the single retry", err)
	}
	if len(f.calls) != 2 || len(f.sleeps) != 1 {
		t.Errorf("requests = %d, sleeps = %d; want 2 and 1", len(f.calls), len(f.sleeps))
	}
}

func TestQueryDoesNotRetryOtherFailures(t *testing.T) {
	cases := map[string]struct {
		reply fakeReply
		want  string
	}{
		"403 without rate-limit signals": {
			reply: fakeReply{Status: 403, Header: headers("X-RateLimit-Remaining", "4000"),
				Body: `{"message":"Resource not accessible by integration"}`},
			want: "http 403",
		},
		"GraphQL error of another type": {
			reply: fakeReply{Body: `{"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a User"}]}`},
			want:  "graphql: Could not resolve to a User",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, f := newFakeClient(t, sequence(tc.reply, fakeReply{Body: okBody}))
			err := c.query(context.Background(), "q", nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if len(f.calls) != 1 || len(f.sleeps) != 0 {
				t.Errorf("requests = %d, sleeps = %d; want 1 and 0", len(f.calls), len(f.sleeps))
			}
		})
	}
}

func TestQueryStopsWhenContextEndsDuringRateLimitSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := newFakeClient(t, func(fakeCall) fakeReply {
		cancel()
		return fakeReply{Status: 429, Header: headers("Retry-After", "60")}
	})
	c.sleep = nil // use the real, ctx-aware wait

	start := time.Now()
	err := c.query(ctx, "q", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("query waited %s after the context ended", waited)
	}
}

func TestQueryReportsRateLimitSleepThroughWarn(t *testing.T) {
	c, _ := newFakeClient(t, sequence(
		fakeReply{Status: 429, Header: headers("Retry-After", "2")},
		fakeReply{Body: okBody}))
	var got []string
	c.warnf = func(format string, args ...any) { got = append(got, format) }
	if err := c.query(context.Background(), "q", nil, nil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "rate-limited") {
		t.Errorf("warnings = %q, want one rate-limit notice", got)
	}
}

func TestTruncateKeepsRuneBoundary(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdef", 3, "abc…"},
		// "é" is two bytes; cutting inside it backs up to before it.
		{"aé", 2, "a…"},
		{"日本語", 4, "日…"},
	}
	for _, tc := range cases {
		if got := truncate([]byte(tc.in), tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
