// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const defaultMetricsAddr = ":9108"

var (
	metricsReg = prometheus.NewRegistry()

	streamUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stream_runner_stream_up",
		Help: "1 if ffmpeg is running for the stream, else 0",
	}, []string{"stream_id"})

	ffmpegRestarts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stream_runner_ffmpeg_restarts_total",
		Help: "Times ffmpeg exited and the worker scheduled a retry",
	}, []string{"stream_id"})

	behindSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stream_runner_behind_seconds",
		Help: "How far media time lags wall time for the live pull (seconds)",
	}, []string{"stream_id"})
)

func init() {
	metricsReg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		streamUp,
		ffmpegRestarts,
		behindSeconds,
	)
}

// MetricsAddrFromEnv returns the listen address.
// Unset → :9108. Empty string → metrics disabled.
func MetricsAddrFromEnv() string {
	v, ok := os.LookupEnv("METRICS_ADDR")
	if !ok {
		return defaultMetricsAddr
	}
	return v
}

// StartMetricsServer serves /metrics. Empty addr is a no-op.
func StartMetricsServer(addr string) {
	if addr == "" {
		slog.Info("metrics disabled (METRICS_ADDR empty)")
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{}))
	go func() {
		slog.Info("metrics listening", "addr", addr, "path", "/metrics")
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("metrics server stopped", "error", err)
		}
	}()
}

func setStreamUp(streamID string, up bool) {
	v := 0.0
	if up {
		v = 1
	}
	streamUp.WithLabelValues(streamID).Set(v)
}

func incFFmpegRestarts(streamID string) {
	ffmpegRestarts.WithLabelValues(streamID).Inc()
}

func setBehindSeconds(streamID string, seconds float64) {
	behindSeconds.WithLabelValues(streamID).Set(seconds)
}

// deleteStreamMetrics drops per-stream series so removed ids do not leak labels.
func deleteStreamMetrics(streamID string) {
	streamUp.DeleteLabelValues(streamID)
	ffmpegRestarts.DeleteLabelValues(streamID)
	behindSeconds.DeleteLabelValues(streamID)
}
