// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestProgressWarnsWhenSourceFallsBehind(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(20 * time.Second)}
	var i int
	now := func() time.Time {
		if i >= len(times) {
			return times[len(times)-1]
		}
		ts := times[i]
		i++
		return ts
	}
	in := strings.NewReader(strings.Join([]string{
		"out_time_us=0",
		"fps=25.0",
		"speed=1.0x",
		"progress=continue",
		"out_time_us=10000000",
		"fps=12.0",
		"speed=0.50x",
		"progress=continue",
		"",
	}, "\n"))
	readProgress("cam-1", in, now, func() pullNet {
		return pullNet{host: "src.example:1935", ip: "203.0.113.5", resolve: 4 * time.Millisecond, dial: 18 * time.Millisecond, resolved: true, dialed: true}
	})
	out := buf.String()
	if !strings.Contains(out, "source lagging") || !strings.Contains(out, "cam-1") || !strings.Contains(out, "behind_s=10.0") || !strings.Contains(out, "dial_ms=18") || !strings.Contains(out, "src_ip=203.0.113.5") || !strings.Contains(out, "resolve_ms=4") {
		t.Fatalf("log:\n%s", out)
	}
}

func TestPullNetworkWarnsWhenDialIsSlow(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	in := strings.NewReader("out_time_us=0\nfps=25.0\nspeed=1.0x\nprogress=continue\n")
	readProgress("cam-1", in, time.Now, func() pullNet {
		return pullNet{host: "src.example:1935", ip: "203.0.113.8", resolve: 5 * time.Millisecond, dial: netWarn, resolved: true, dialed: true}
	})
	out := buf.String()
	if !strings.Contains(out, "pull network") || !strings.Contains(out, "dial_ms=300") || !strings.Contains(out, "src_host=src.example:1935") {
		t.Fatalf("log:\n%s", out)
	}
}
