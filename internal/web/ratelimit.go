package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket: burst tokens up front, then one
// more every interval.
type rateLimiter struct {
	burst    float64
	interval time.Duration
	now      func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxBuckets is a hard cap on tracked clients. Past it, idle buckets are
// swept at most once per sweepEvery; while the table is still full, a new
// client evicts the fullest of evictSample buckets picked from map
// iteration instead of being refused, so a flood of fresh keys cannot lock
// every new visitor out. Memory stays bounded, no request pays for a scan
// of the whole table more than once a minute, and an evicted client merely
// gets a fresh burst back.
const (
	maxBuckets  = 10000
	sweepEvery  = time.Minute
	evictSample = 8
)

func newRateLimiter(burst int, interval time.Duration) *rateLimiter {
	return &rateLimiter{
		burst:    float64(burst),
		interval: interval,
		now:      time.Now,
		buckets:  map[string]*bucket{},
	}
}

// Allow spends one token for key, reporting false when none is left.
func (l *rateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets && now.Sub(l.lastSweep) >= sweepEvery {
			l.sweepLocked(now)
		}
		if len(l.buckets) >= maxBuckets {
			l.evictLocked(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = l.refilled(b, now)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked drops buckets that have refilled completely; they hold no
// state a fresh bucket would not.
func (l *rateLimiter) sweepLocked(now time.Time) {
	l.lastSweep = now
	full := time.Duration(l.burst * float64(l.interval))
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
}

// evictLocked drops the bucket holding the most tokens among the first
// evictSample that map iteration yields: it is the one closest to the
// state a fresh bucket would have, so dropping it gives away the least.
func (l *rateLimiter) evictLocked(now time.Time) {
	var victim string
	most, n := -1.0, 0
	for k, b := range l.buckets {
		if t := l.refilled(b, now); t > most {
			victim, most = k, t
		}
		if n++; n >= evictSample {
			break
		}
	}
	delete(l.buckets, victim)
}

// refilled is the token count b would hold at now, before spending.
func (l *rateLimiter) refilled(b *bucket, now time.Time) float64 {
	return min(b.tokens+now.Sub(b.last).Seconds()/l.interval.Seconds(), l.burst)
}

// clientKey is the rate-limit key for the submitting client: its IPv4
// address, or its IPv6 /64, since one subscriber is routinely handed a
// whole /64 and can rotate through it at will. A peer on a loopback or
// private address is taken to be the reverse proxy (Coolify's Traefik), so
// the last X-Forwarded-For hop (the one the proxy itself appended) wins.
// A public peer is the client and its header is ignored.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr := peer
	if peer.IsLoopback() || peer.IsPrivate() {
		if vals := r.Header.Values("X-Forwarded-For"); len(vals) > 0 {
			hops := strings.Split(vals[len(vals)-1], ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
				addr = ip
			}
		}
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}
