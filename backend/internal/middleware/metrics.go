package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shopen/backend/internal/metrics"
)

// responseWriter captures HTTP status codes
type responseWriter struct {
	http.ResponseWriter
	status int
}

// override WriteHeader to capture status
func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// MetricsMiddleware records request metrics for Prometheus
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		start := time.Now()

		// wrap response writer
		rw := &responseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
		}

		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()

		// use route pattern instead of raw path
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = r.URL.Path
		}

		statusCode := strconv.Itoa(rw.status)

		// record latency
		metrics.RequestLatency.
			WithLabelValues(r.Method, routePattern).
			Observe(duration)

		// record request count
		metrics.RequestCount.
			WithLabelValues(r.Method, routePattern, statusCode).
			Inc()
	})
}

// expose Prometheus metrics endpoint
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}
