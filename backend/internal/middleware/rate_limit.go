package middleware

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shopen/backend/internal/cache"
)

const (
	requestLimit = 100
	window       = time.Minute
)

// RateLimit middleware limits requests per IP using Redis.
func RateLimit(next http.Handler) http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		ip := getClientIP(r)

		key := "ratelimit:" + ip

		count, err := cache.Client.Incr(cache.Ctx, key).Result()
		if err != nil {
			http.Error(w, "rate limiter error", http.StatusInternalServerError)
			return
		}

		// set expiry only when key is first created
		if count == 1 {
			cache.Client.Expire(cache.Ctx, key, window)
		}

		if count > requestLimit {

			w.Header().Set("Retry-After", "60")

			http.Error(w, "too many requests", http.StatusTooManyRequests)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// getClientIP extracts the real client IP address
func getClientIP(r *http.Request) string {

	// 1️⃣ Check X-Forwarded-For header
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {

		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}

	// 2️⃣ Check X-Real-IP header
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}

	// 3️⃣ Fallback to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return ip
}
