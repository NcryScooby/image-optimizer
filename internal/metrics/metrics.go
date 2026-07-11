// Package metrics registers Prometheus metrics for the image-optimizer.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "image_optimizer"

// Result label values for jobs_processed_total and worker_job_duration_seconds.
const (
	ResultSuccess  = "success"
	ResultFailed   = "failed"
	ResultRequeued = "requeued"
)

var (
	// API cache / enqueue counters.
	CacheHitsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "cache_hits_total",
		Help:      "GET requests that served a ready variant",
	})
	CacheMissesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "cache_misses_total",
		Help:      "Cold cache misses that upserted a variant and published a job",
	})
	CachePendingTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "cache_pending_total",
		Help:      "GET polls that found a pending variant",
	})
	CacheFailedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "cache_failed_total",
		Help:      "GET requests that found a failed variant",
	})
	JobsEnqueuedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "jobs_enqueued_total",
		Help:      "Jobs successfully published to the queue",
	})

	// Worker metrics.
	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "queue_depth",
		Help:      "Approximate number of messages in the image.variants queue",
	})
	JobsProcessedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "jobs_processed_total",
		Help:      "Worker jobs finished by result",
	}, []string{"result"})
	WorkerJobDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "worker_job_duration_seconds",
		Help:      "End-to-end worker job duration in seconds",
	}, []string{"result"})
	WorkerImgproxyFetchDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "worker_imgproxy_fetch_duration_seconds",
		Help:      "Duration of imgproxy fetch calls in seconds",
	})
	WorkerDiskWriteDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "worker_disk_write_duration_seconds",
		Help:      "Duration of variant disk writes in seconds",
	})
)

func init() {
	prometheus.MustRegister(
		CacheHitsTotal,
		CacheMissesTotal,
		CachePendingTotal,
		CacheFailedTotal,
		JobsEnqueuedTotal,
		QueueDepth,
		JobsProcessedTotal,
		WorkerJobDurationSeconds,
		WorkerImgproxyFetchDurationSeconds,
		WorkerDiskWriteDurationSeconds,
	)

	// Pre-create known label combinations so they appear at scrape with zero.
	for _, result := range []string{ResultSuccess, ResultFailed, ResultRequeued} {
		JobsProcessedTotal.WithLabelValues(result)
		WorkerJobDurationSeconds.WithLabelValues(result)
	}
}
