package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type rateLimitEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimit returns a per-IP rate limiting middleware using a token bucket algorithm.
// rps is the sustained request rate and burst is the maximum burst size.
// Entries not seen in 10 minutes are evicted every minute to prevent memory leaks.
func RateLimit(rps float64, burst int) func(http.Handler) http.Handler {
	var mu sync.Mutex
	visitors := make(map[string]*rateLimitEntry)

	// Background goroutine to evict stale entries.
	go func() {
		for range time.Tick(time.Minute) {
			mu.Lock()
			for ip, e := range visitors {
				if time.Since(e.lastSeen) > 10*time.Minute {
					delete(visitors, ip)
				}
			}
			mu.Unlock()
		}
	}()

	getLimiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		e, ok := visitors[ip]
		if !ok {
			e = &rateLimitEntry{
				limiter: rate.NewLimiter(rate.Limit(rps), burst),
			}
			visitors[ip] = e
		}
		e.lastSeen = time.Now()
		return e.limiter
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if !getLimiter(ip).Allow() {
				http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
