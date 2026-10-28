package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginLimiter counts failed sign-ins per key (a client address, a user name) over a
// sliding window. Past the limit, sign-ins for that key are refused with 429 until the
// oldest failure ages out. Tokens already issued keep working throughout, so a stranger
// hammering one account cannot sign its owner's devices out.
type loginLimiter struct {
	window time.Duration
	max    int

	mu    sync.Mutex
	fails map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{window: window, max: max, fails: map[string][]time.Time{}}
}

// retryAfter is how long key must wait before trying again, or 0 when it may try now.
func (l *loginLimiter) retryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.prune(key, now)
	if len(recent) < l.max {
		return 0
	}
	return recent[len(recent)-l.max].Add(l.window).Sub(now)
}

// try counts an attempt for key unless key is over the limit, in one step, so a burst
// of parallel attempts cannot all pass the check before any is counted. It returns
// how long to wait (and counts nothing) when over the limit.
func (l *loginLimiter) try(key string, now time.Time) time.Duration {
	l.mu.Lock()
	recent := l.prune(key, now)
	if len(recent) >= l.max {
		wait := recent[len(recent)-l.max].Add(l.window).Sub(now)
		l.mu.Unlock()
		return wait
	}
	l.mu.Unlock()
	l.fail(key, now)
	return 0
}

func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.prune(key, now), now)
	// Keep the table from growing without bound under a spray of addresses.
	if len(l.fails) > 10000 {
		for k := range l.fails {
			if len(l.prune(k, now)) == 0 {
				delete(l.fails, k)
			}
		}
	}
}

// forget takes back the attempt counted at t (it turned out to be right).
func (l *loginLimiter) forget(key string, t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.fails[key]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Equal(t) {
			l.fails[key] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(l.fails[key]) == 0 {
		delete(l.fails, key)
	}
}

// prune drops failures older than the window; the caller holds mu.
func (l *loginLimiter) prune(key string, now time.Time) []time.Time {
	list := l.fails[key]
	cut := 0
	for cut < len(list) && now.Sub(list[cut]) >= l.window {
		cut++
	}
	list = list[cut:]
	if len(list) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = list
	return list
}

// clientIP is the address a request came from. Behind a reverse proxy every request
// comes from the proxy, so with trustProxy the last X-Forwarded-For hop (the one the
// proxy added) is used instead. Only set that when a proxy is in front: otherwise
// clients could pick their own address.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			hops := strings.Split(xff, ",")
			if ip := strings.TrimSpace(hops[len(hops)-1]); ip != "" {
				return ip
			}
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
