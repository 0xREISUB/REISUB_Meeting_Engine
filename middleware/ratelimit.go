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
	return LimitStatus(limit, window, http.StatusNotFound)
}

// LimitStatus allows at most limit answers with one of statuses per client
// IP in each fixed window; after that every request from that IP gets 429
// rate_limited until the window ends. Other answers are not counted, so it
// stops guessing (wrong passwords, unknown codes) without limiting use.
func LimitStatus(limit int, window time.Duration, statuses ...int) gin.HandlerFunc {
	return LimitStatusWith(NewKeyLimiter(limit, window), statuses...)
}

// LimitStatusWith is LimitStatus counting in k, per client IP, so that
// something outside HTTP (the realtime connection) can count in the same
// window.
func LimitStatusWith(k *KeyLimiter, statuses ...int) gin.HandlerFunc {
	l := k.l
	return func(c *gin.Context) {
		t, ip := now(), c.ClientIP()
		if over, retryAfter := l.exhausted(ip, t); over {
			abortRateLimited(c, retryAfter)
			return
		}
		c.Next()
		for _, status := range statuses {
			if c.Writer.Status() == status {
				l.add(ip, t)
				return
			}
		}
	}
}

// KeyLimiter counts events per key, such as failed logins per nick, across
// every address: it holds back guessing spread over many.
type KeyLimiter struct{ l *limiter }

func NewKeyLimiter(limit int, window time.Duration) *KeyLimiter {
	return &KeyLimiter{l: newLimiter(limit, window)}
}

// Exhausted reports whether key has used up its window, and when it resets.
func (k *KeyLimiter) Exhausted(key string) (bool, time.Duration) {
	return k.l.exhausted(key, now())
}

func (k *KeyLimiter) Add(key string) { k.l.add(key, now()) }

// Reset forgets key's count, as after a successful login.
func (k *KeyLimiter) Reset(key string) {
	k.l.mu.Lock()
	defer k.l.mu.Unlock()
	delete(k.l.counts, key)
}

// AbortRateLimited answers 429 rate_limited with Retry-After.
func AbortRateLimited(c *gin.Context, retryAfter time.Duration) {
	abortRateLimited(c, retryAfter)
}
