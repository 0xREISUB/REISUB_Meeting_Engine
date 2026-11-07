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

// RateLimit allows at most limit requests per client IP in each fixed
// window and answers the rest with 429 rate_limited and Retry-After. The
// client IP honours TRUSTED_PROXIES (see main.go). Counters live in memory
// and reset on restart.
func RateLimit(limit int, window time.Duration) gin.HandlerFunc {
	type counter struct {
		start time.Time
		count int
	}
	var mu sync.Mutex
	counters := map[string]*counter{}
	return func(c *gin.Context) {
		t := now()
		ip := c.ClientIP()
		mu.Lock()
		// Bound memory: drop finished windows once many addresses are tracked.
		if len(counters) > 10000 {
			for key, entry := range counters {
				if t.Sub(entry.start) >= window {
					delete(counters, key)
				}
			}
		}
		entry := counters[ip]
		if entry == nil || t.Sub(entry.start) >= window {
			entry = &counter{start: t}
			counters[ip] = entry
		}
		entry.count++
		over := entry.count > limit
		retryAfter := entry.start.Add(window).Sub(t)
		mu.Unlock()
		if over {
			c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			apierror.Abort(c, http.StatusTooManyRequests, apierror.RateLimited, "Çok fazla istek gönderildi. Lütfen biraz sonra tekrar deneyin.")
			return
		}
		c.Next()
	}
}
