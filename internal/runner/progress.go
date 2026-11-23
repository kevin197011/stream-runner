// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"time"
)

const (
	statsPeriod = 10 * time.Second
	// lagWarn is how far media time may fall behind wall time before a warning.
	// This is relay pace, not camera-to-screen latency.
	lagWarn     = 3 * time.Second
	dialTimeout = 2 * time.Second
	// netWarn is a slow TCP handshake to the pull source.
	netWarn = 300 * time.Millisecond
)

// pullNet is one probe of the path to the pull source.
// ponytail: fresh connect, not the live RTMP socket RTT. Use that socket's TCP_INFO if this number disagrees with the path.
type pullNet struct {
	host     string
	ip       string
	resolve  time.Duration
	dial     time.Duration
	resolved bool
	dialed   bool
	err      error
}

func (n pullNet) logAttrs() []any {
	resolveMS := ""
	if n.resolved {
		resolveMS = fmt.Sprintf("%.0f", float64(n.resolve)/float64(time.Millisecond))
	}
	dialMS := ""
	if n.dialed {
		dialMS = fmt.Sprintf("%.0f", float64(n.dial)/float64(time.Millisecond))
	}
	attrs := []any{
		"src_host", n.host,
		"src_ip", n.ip,
		"resolve_ms", resolveMS,
		"dial_ms", dialMS,
	}
	if n.err != nil {
		attrs = append(attrs, "dial_error", n.err.Error())
	}
	return attrs
}

func (n pullNet) slow() bool {
	return n.err != nil || (n.dialed && n.dial >= netWarn)
}

func probePull(src string) pullNet {
	var out pullNet
	u, err := url.Parse(src)
	if err != nil || u.Hostname() == "" {
		out.err = fmt.Errorf("bad source url")
		return out
	}
	port := u.Port()
	if port == "" {
		port = "1935"
	}
	out.host = net.JoinHostPort(u.Hostname(), port)
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	start := time.Now()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	out.resolve = time.Since(start)
	out.resolved = true
	if err != nil {
		out.err = err
		return out
	}
	if len(ips) == 0 {
		out.err = fmt.Errorf("no address")
		return out
	}
	out.ip = ips[0].String()
	start = time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(out.ip, port), dialTimeout)
	out.dial = time.Since(start)
	out.dialed = true
	if err != nil {
		out.err = err
		return out
	}
	_ = conn.Close()
	return out
}

// paceTracker compares media PTS deltas to wall time and logs every statsPeriod.
// Incremental behind avoids a bad first PTS (e.g. audio at 0, video already large).
type paceTracker struct {
	streamID string
	probe    func() pullNet
	now      func() time.Time

	mu        sync.Mutex
	lastWall  time.Time
	lastMedia time.Duration
	behind    time.Duration
	haveBase  bool
}

func newPaceTracker(streamID string, probe func() pullNet) *paceTracker {
	return &paceTracker{streamID: streamID, probe: probe, now: time.Now}
}

func (p *paceTracker) note(pts time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.now()
	if !p.haveBase {
		p.lastWall, p.lastMedia, p.haveBase = t, pts, true
		return
	}
	if t.Sub(p.lastWall) < statsPeriod {
		return
	}
	gap := t.Sub(p.lastWall)
	mediaDelta := pts - p.lastMedia
	// PTS reset / track switch: ignore this window's media jump, keep wall gap.
	if mediaDelta < 0 || mediaDelta > gap*4 {
		mediaDelta = gap
	}
	p.behind += gap - mediaDelta
	// Startup/buffer lead is not lag; floor at 0 for the metric.
	if p.behind < 0 {
		p.behind = 0
	}
	speed := ""
	if gap > 0 {
		speed = fmt.Sprintf("%.2fx", float64(mediaDelta)/float64(gap))
	}
	p.lastWall = t
	p.lastMedia = pts
	setBehindSeconds(p.streamID, p.behind.Seconds())
	netSample := p.probe()
	attrs := []any{
		"stream_id", p.streamID,
		"behind_s", fmt.Sprintf("%.1f", p.behind.Seconds()),
		"speed", speed,
		"gap_s", fmt.Sprintf("%.1f", gap.Seconds()),
	}
	attrs = append(attrs, netSample.logAttrs()...)
	switch {
	case p.behind >= lagWarn || gap > statsPeriod*2:
		slog.Warn("source lagging", attrs...)
	case netSample.slow():
		slog.Warn("pull network", attrs...)
	default:
		slog.Info("source pace", attrs...)
	}
}
