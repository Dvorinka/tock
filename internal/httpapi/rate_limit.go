package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxTrackedRateLimitIPs = 10_000

type rateLimitWindow struct {
	started time.Time
	count   int
}

// ipRateLimiter applies a fixed-window limit per remote IP. It deliberately
// uses RemoteAddr instead of forwarded headers, which are client-controlled
// unless a trusted-proxy list is configured.
type ipRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string]rateLimitWindow
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		limit:   limit,
		window:  window,
		clients: make(map[string]rateLimitWindow),
	}
}

func (l *ipRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		now := time.Now()
		ip := rateLimitClientIP(r.RemoteAddr)
		l.mu.Lock()
		bucket, ok := l.clients[ip]
		if !ok || now.Sub(bucket.started) >= l.window {
			if !ok && len(l.clients) >= maxTrackedRateLimitIPs {
				l.evictOldest()
			}
			bucket = rateLimitWindow{started: now}
		}
		if bucket.count >= l.limit {
			retryAfter := time.Until(bucket.started.Add(l.window))
			l.mu.Unlock()
			seconds := int(retryAfter.Seconds())
			if retryAfter > time.Duration(seconds)*time.Second {
				seconds++
			}
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeMessage(w, http.StatusTooManyRequests, "Too many requests. Please try again later.")
			return
		}
		bucket.count++
		l.clients[ip] = bucket
		l.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

func (l *ipRateLimiter) evictOldest() {
	var oldestIP string
	var oldest time.Time
	for ip, bucket := range l.clients {
		if oldestIP == "" || bucket.started.Before(oldest) {
			oldestIP, oldest = ip, bucket.started
		}
	}
	delete(l.clients, oldestIP)
}

func rateLimitClientIP(remoteAddr string) string {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return ip
	}
	return strings.Trim(remoteAddr, "[]")
}
