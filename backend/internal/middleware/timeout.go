package middleware

import (
	"net/http"
	"time"
)

// TimeoutMiddleware cancels requests that take too long
func TimeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {

	return func(next http.Handler) http.Handler {

		return http.TimeoutHandler(next, timeout, "request timeout\n")
	}
}