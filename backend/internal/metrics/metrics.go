package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	once sync.Once

	RequestCount   *prometheus.CounterVec
	RequestLatency *prometheus.HistogramVec
)

// Init initializes Prometheus metrics.
// Safe to call multiple times.
func Init() {

	once.Do(func() {

		RequestCount = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "shopen",
				Subsystem: "http",
				Name:      "requests_total",
				Help:      "Total number of HTTP requests",
			},
			[]string{"method", "path", "status"},
		)

		RequestLatency = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "shopen",
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "HTTP request latency in seconds",

				// Better buckets for APIs
				Buckets: []float64{
					0.005,
					0.01,
					0.025,
					0.05,
					0.1,
					0.25,
					0.5,
					1,
					2,
					5,
				},
			},
			[]string{"method", "path"},
		)

		prometheus.MustRegister(RequestCount)
		prometheus.MustRegister(RequestLatency)
	})
}
