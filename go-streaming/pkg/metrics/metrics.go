package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HTTPRequestsTotal tracks the total count of incoming HTTP requests
	HTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bitmill",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests processed by Bitmill API Server",
		},
		[]string{"method", "path", "status"},
	)

	// HTTPRequestDuration tracks latency of HTTP requests in seconds
	HTTPRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bitmill",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Histogram of HTTP request latencies",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path"},
	)

	// TranscodeJobsTotal tracks transcode tasks processed by workers
	TranscodeJobsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bitmill",
			Subsystem: "transcoder",
			Name:      "jobs_total",
			Help:      "Total number of transcode jobs completed by worker daemons",
		},
		[]string{"status", "source_resolution"},
	)

	// TranscodeDuration tracks transcoding duration per video
	TranscodeDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bitmill",
			Subsystem: "transcoder",
			Name:      "duration_seconds",
			Help:      "Duration of media transcoding pipeline in seconds",
			Buckets:   []float64{1, 5, 10, 30, 60, 120, 300, 600, 1200},
		},
		[]string{"source_resolution"},
	)
)

// PrometheusMiddleware returns a Gin middleware capturing request count and latency
func PrometheusMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Do not record metrics for the /metrics endpoint itself to avoid recursion
		if c.Request.URL.Path == "/metrics" {
			c.Next()
			return
		}

		start := time.Now()
		c.Next()

		duration := time.Since(start).Seconds()
		statusStr := strconv.Itoa(c.Writer.Status())
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		HTTPRequestsTotal.WithLabelValues(c.Request.Method, path, statusStr).Inc()
		HTTPRequestDuration.WithLabelValues(c.Request.Method, path).Observe(duration)
	}
}
