package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"reisub-backend/apierror"

	"github.com/gin-gonic/gin"
)

// now is replaceable in tests.
var now = time.Now

// limiter counts events per client IP in fixed windows. Counters live in
// memory and reset on restart.
type limiter struct {
	limit  int
	window time.Duration
	mu     sync.Mutex
	counts map[string]*windowCount
}

type windowCount struct {
	start time.Time
	count int
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, counts: map[string]*windowCount{}}
}

// entry returns ip's counter for the current window. Callers hold mu.
func (l *limiter) entry(ip string, t time.Time) *windowCount {
	// Bound memory: drop finished windows once many addresses are tracked.
	if len(l.counts) > 10000 {
		for key, entry := range l.counts {
			if t.Sub(entry.start) >= l.window {
				delete(l.counts, key)
			}
		}
	}
	entry := l.counts[ip]
	if entry == nil || t.Sub(entry.start) >= l.window {
		entry = &windowCount{start: t}
		l.counts[ip] = entry
	}
	return entry
}

// exhausted reports whether ip has used up its window, and when it resets.
func (l *limiter) exhausted(ip string, t time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entry(ip, t)
	return entry.count >= l.limit, entry.start.Add(l.window).Sub(t)
}

func (l *limiter) add(ip string, t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entry(ip, t).count++
}

func abortRateLimited(c *gin.Context, retryAfter time.Duration) {
	c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
	apierror.Abort(c, http.StatusTooManyRequests, apierror.RateLimited, "Çok fazla istek gönderildi. Lütfen biraz sonra tekrar deneyin.")
}

// RateLimit allows at most limit requests per client IP in each fixed
// window and answers the rest with 429 rate_limited and Retry-After. The
// client IP honours TRUSTED_PROXIES (see main.go).
func RateLimit(limit int, window time.Duration) gin.HandlerFunc {
	l := newLimiter(limit, window)
	return func(c *gin.Context) {
		t, ip := now(), c.ClientIP()
		if over, retryAfter := l.exhausted(ip, t); over {
			abortRateLimited(c, retryAfter)
			return
		}
		l.add(ip, t)
		c.Next()
	}
}

// LimitNotFound allows at most limit 404 answers per client IP in each fixed
// window; after that every request from that IP gets 429 rate_limited until
// the window ends. It stops guessing room codes, which a 404 reveals as
// wrong, without limiting people who use the rooms they know.
func LimitNotFound(limit int, window time.Duration) gin.HandlerFunc {
	l := newLimiter(limit, window)
	return func(c *gin.Context) {
		t, ip := now(), c.ClientIP()
		if over, retryAfter := l.exhausted(ip, t); over {
			abortRateLimited(c, retryAfter)
			return
		}
		c.Next()
		if c.Writer.Status() == http.StatusNotFound {
			l.add(ip, t)
		}
	}
}
