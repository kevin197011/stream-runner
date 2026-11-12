// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestMetricsAddrFromEnv(t *testing.T) {
	os.Unsetenv("METRICS_ADDR")
	if MetricsAddrFromEnv() != defaultMetricsAddr {
		t.Fatalf("unset want %q", defaultMetricsAddr)
	}
	t.Setenv("METRICS_ADDR", "")
	if MetricsAddrFromEnv() != "" {
		t.Fatal("empty should disable")
	}
	t.Setenv("METRICS_ADDR", ":9111")
	if MetricsAddrFromEnv() != ":9111" {
		t.Fatal("custom addr")
	}
}

func TestMetricsExposeAndDelete(t *testing.T) {
	id := "metrics-test-stream"
	deleteStreamMetrics(id)

	setStreamUp(id, true)
	incFFmpegRestarts(id)
	setBehindSeconds(id, 1.5)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	out := string(body)
	for _, want := range []string{
		`stream_runner_stream_up{stream_id="metrics-test-stream"} 1`,
		`stream_runner_ffmpeg_restarts_total{stream_id="metrics-test-stream"} 1`,
		`stream_runner_behind_seconds{stream_id="metrics-test-stream"} 1.5`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}

	deleteStreamMetrics(id)
	rec = httptest.NewRecorder()
	promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
	out = rec.Body.String()
	if strings.Contains(out, `stream_id="metrics-test-stream"`) {
		t.Fatal("label series should be gone after delete")
	}
}
