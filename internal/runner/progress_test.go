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

func TestPaceWarnsWhenSourceFallsBehind(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	// Wall +20s, media only +10s → behind 10s.
	times := []time.Time{base, base.Add(20 * time.Second)}
	pts := []time.Duration{0, 10 * time.Second}
	var i int
	p := &paceTracker{
		streamID: "cam-1",
		probe: func() pullNet {
			return pullNet{host: "src.example:1935", ip: "203.0.113.5", resolve: 4 * time.Millisecond, dial: 18 * time.Millisecond, resolved: true, dialed: true}
		},
		now: func() time.Time {
			ts := times[i]
			if i < len(times)-1 {
				i++
			}
			return ts
		},
	}
	p.note(pts[0])
	p.note(pts[1])
	out := buf.String()
	if !strings.Contains(out, "source lagging") || !strings.Contains(out, "cam-1") || !strings.Contains(out, "behind_s=10.0") || !strings.Contains(out, "dial_ms=18") {
		t.Fatalf("log:\n%s", out)
	}
}

func TestPaceIgnoresHugePTSJump(t *testing.T) {
	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(statsPeriod)}
	var i int
	p := &paceTracker{
		streamID: "cam-1",
		probe:    func() pullNet { return pullNet{} },
		now: func() time.Time {
			ts := times[i]
			if i < len(times)-1 {
				i++
			}
			return ts
		},
	}
	p.note(0)
	p.note(70 * time.Second) // audio→video jump; treat as realtime
	if p.behind != 0 {
		t.Fatalf("behind=%v want 0 after ignored jump", p.behind)
	}
}

func TestPacePullNetworkWarnsWhenDialIsSlow(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	base := time.Now()
	times := []time.Time{base, base.Add(statsPeriod)}
	var i int
	p := &paceTracker{
		streamID: "cam-1",
		probe: func() pullNet {
			return pullNet{host: "src.example:1935", ip: "203.0.113.8", resolve: 5 * time.Millisecond, dial: netWarn, resolved: true, dialed: true}
		},
		now: func() time.Time {
			ts := times[i]
			if i < len(times)-1 {
				i++
			}
			return ts
		},
	}
	p.note(0)
	p.note(statsPeriod)
	out := buf.String()
	if !strings.Contains(out, "pull network") || !strings.Contains(out, "dial_ms=300") {
		t.Fatalf("log:\n%s", out)
	}
}
