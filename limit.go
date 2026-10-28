package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// loginLimiter counts password attempts per key (a client address, a user name) over a
// sliding window; a right password takes its attempt back, so what remains are the
// failures. Past the limit, attempts for that key are refused with 429 until the oldest
// ages out. Tokens already issued keep working throughout, so a stranger hammering one
// account cannot sign its owner's devices out.
type loginLimiter struct {
	window time.Duration
	max    int

	mu        sync.Mutex
	fails     map[string][]time.Time
	lastSweep time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{window: window, max: max, fails: map[string][]time.Time{}}
}

// try counts an attempt for key unless key is over the limit, all under one lock, so a
// burst of parallel attempts cannot pass the check before any is counted. Over the limit
// it counts nothing and returns how long to wait.
func (l *loginLimiter) try(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.prune(key, now)
	if len(recent) >= l.max {
		return recent[len(recent)-l.max].Add(l.window).Sub(now)
	}
	l.fails[key] = append(recent, now)
	// Keep the table from growing without bound under a spray of addresses, sweeping at
	// most once a minute so a spray cannot make every attempt walk the whole table.
	if len(l.fails) > 10000 && now.Sub(l.lastSweep) > time.Minute {
		l.lastSweep = now
		for k := range l.fails {
			l.prune(k, now)
		}
	}
	return 0
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

// prune drops attempts older than the window; the caller holds mu.
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

// limitKey is the address an attempt counts against: IPv6 clients by their /64 network,
// since one subscriber gets a whole /64 and could otherwise try from endless addresses.
func limitKey(ip string) string {
	if a := net.ParseIP(ip); a != nil && a.To4() == nil {
		return a.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return ip
}

// knownClients remembers which addresses have signed in to which accounts, for a month.
// The per-account limit does not apply to them: someone guessing at an account can
// stop new sign-ins to it from elsewhere for a while, but not from the owner's own
// networks (whose addresses still have their own, per-address limit).
type knownClients struct {
	mu   sync.Mutex
	seen map[string]time.Time // user + "\x00" + address key -> last sign-in
}

const knownFor = 30 * 24 * time.Hour

func newKnownClients() *knownClients { return &knownClients{seen: map[string]time.Time{}} }

func (k *knownClients) has(user, key string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	t, ok := k.seen[strings.ToLower(user)+"\x00"+key]
	return ok && now.Sub(t) < knownFor
}

func (k *knownClients) add(user, key string, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.seen) > 10000 {
		for id, t := range k.seen {
			if now.Sub(t) >= knownFor {
				delete(k.seen, id)
			}
		}
		if len(k.seen) > 10000 {
			clear(k.seen)
		}
	}
	k.seen[strings.ToLower(user)+"\x00"+key] = now
}
