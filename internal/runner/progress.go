// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
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

func parseOutTime(v string) (time.Duration, bool) {
	parts := strings.SplitN(v, ":", 3)
	if len(parts) != 3 {
		return 0, false
	}
	hour, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	d := time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute + time.Duration(sec*float64(time.Second))
	return d, true
}

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
	if cerr := conn.Close(); cerr != nil {
		// Handshake finished. A close error is not path delay.
	}
	return out
}

// readProgress logs source pace and the pull-path probe.
// behind_s grows when media arrives slower than realtime.
func readProgress(streamID string, r io.Reader, now func() time.Time, probe func() pullNet) {
	sc := bufio.NewScanner(r)
	var (
		media     time.Duration
		haveMedia bool
		speed     string
		fps       string
		wall0     time.Time
		media0    time.Duration
		lastWall  time.Time
		haveBase  bool
	)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_us":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				continue
			}
			media = time.Duration(n) * time.Microsecond
			haveMedia = true
		case "out_time":
			if haveMedia {
				continue
			}
			d, ok := parseOutTime(val)
			if !ok {
				continue
			}
			media = d
			haveMedia = true
		case "fps":
			fps = val
		case "speed":
			speed = strings.TrimSpace(val)
		case "progress":
			if (val != "continue" && val != "end") || !haveMedia {
				continue
			}
			t := now()
			if !haveBase {
				wall0, media0, lastWall, haveBase = t, media, t, true
			}
			behind := t.Sub(wall0) - (media - media0)
			gap := t.Sub(lastWall)
			lastWall = t
			netSample := probe()
			attrs := []any{
				"stream_id", streamID,
				"behind_s", fmt.Sprintf("%.1f", behind.Seconds()),
				"speed", speed,
				"fps", fps,
				"gap_s", fmt.Sprintf("%.1f", gap.Seconds()),
			}
			attrs = append(attrs, netSample.logAttrs()...)
			switch {
			case behind >= lagWarn || gap > statsPeriod*2:
				slog.Warn("source lagging", attrs...)
			case netSample.slow():
				slog.Warn("pull network", attrs...)
			default:
				slog.Info("source pace", attrs...)
			}
			haveMedia = false
			speed, fps = "", ""
		}
	}
}
