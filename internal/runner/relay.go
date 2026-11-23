// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
)

// relayOnce pulls src and republishes to dst until ctx is done or the link fails.
// Tests replace this.
var relayOnce = defaultRelayOnce

// normalizeRTMPURL maps classic rtmp://host/app/stream to gortmplib's app#stream.
// Fragment URLs are left alone. Single-segment paths stay as app-only.
func normalizeRTMPURL(u *url.URL) *url.URL {
	if u.Fragment != "" {
		return u
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return u
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return u
	}
	nu := *u
	nu.Path = "/" + strings.Join(parts[:len(parts)-1], "/")
	nu.RawPath = ""
	nu.Fragment = parts[len(parts)-1]
	return &nu
}

func parseRTMPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "rtmp", "rtmps":
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host")
	}
	return normalizeRTMPURL(u), nil
}

func defaultRelayOnce(ctx context.Context, streamID, src, dst string) error {
	srcURL, err := parseRTMPURL(src)
	if err != nil {
		return fmt.Errorf("src: %w", err)
	}
	dstURL, err := parseRTMPURL(dst)
	if err != nil {
		return fmt.Errorf("dst: %w", err)
	}

	player := &gortmplib.Client{URL: srcURL, Publish: false}
	if err := player.Initialize(ctx); err != nil {
		return fmt.Errorf("play connect: %w", err)
	}
	defer player.Close()

	publisher := &gortmplib.Client{URL: dstURL, Publish: true}
	if err := publisher.Initialize(ctx); err != nil {
		return fmt.Errorf("publish connect: %w", err)
	}
	defer publisher.Close()

	go func() {
		<-ctx.Done()
		player.Close()
		publisher.Close()
	}()

	if err := player.NetConn().SetReadDeadline(time.Now().Add(rwTimeout)); err != nil {
		return err
	}
	reader := &gortmplib.Reader{Conn: player}
	if err := reader.Initialize(); err != nil {
		return fmt.Errorf("reader: %w", err)
	}
	if len(reader.Tracks()) == 0 {
		return fmt.Errorf("no tracks on source")
	}

	if err := publisher.NetConn().SetReadDeadline(time.Now().Add(rwTimeout)); err != nil {
		return err
	}
	writer := &gortmplib.Writer{Conn: publisher, Tracks: reader.Tracks()}
	if err := writer.Initialize(); err != nil {
		return fmt.Errorf("writer: %w", err)
	}

	slog.Info("relaying", "stream_id", streamID, "tracks", len(reader.Tracks()))

	var writeErr atomic.Pointer[error]
	pace := newPaceTracker(streamID, func() pullNet { return probePull(src) })
	fail := func(err error) {
		if err == nil {
			return
		}
		writeErr.CompareAndSwap(nil, &err)
		player.Close()
		publisher.Close()
	}
	if err := wireTracks(reader, writer, pace, fail); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := player.NetConn().SetReadDeadline(time.Now().Add(rwTimeout)); err != nil {
			return err
		}
		if err := reader.Read(); err != nil {
			if e := writeErr.Load(); e != nil {
				return *e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func wireTracks(
	reader *gortmplib.Reader,
	writer *gortmplib.Writer,
	pace *paceTracker,
	fail func(error),
) error {
	for _, track := range reader.Tracks() {
		switch track.Codec.(type) {
		case *codecs.AV1:
			reader.OnDataAV1(track, func(pts time.Duration, tu [][]byte) {
				pace.note(pts)
				fail(writer.WriteAV1(track, pts, tu))
			})
		case *codecs.VP9:
			reader.OnDataVP9(track, func(pts time.Duration, frame []byte) {
				pace.note(pts)
				fail(writer.WriteVP9(track, pts, frame))
			})
		case *codecs.H265:
			reader.OnDataH265(track, func(pts, dts time.Duration, au [][]byte) {
				pace.note(pts)
				fail(writer.WriteH265(track, pts, dts, au))
			})
		case *codecs.H264:
			reader.OnDataH264(track, func(pts, dts time.Duration, au [][]byte) {
				pace.note(pts)
				fail(writer.WriteH264(track, pts, dts, au))
			})
		case *codecs.Opus:
			reader.OnDataOpus(track, func(pts time.Duration, packet []byte) {
				fail(writer.WriteOpus(track, pts, packet))
			})
		case *codecs.FLAC:
			reader.OnDataFLAC(track, func(pts time.Duration, frame []byte) {
				fail(writer.WriteFLAC(track, pts, frame))
			})
		case *codecs.MPEG4Audio:
			reader.OnDataMPEG4Audio(track, func(pts time.Duration, au []byte) {
				fail(writer.WriteMPEG4Audio(track, pts, au))
			})
		case *codecs.MPEG1Audio:
			reader.OnDataMPEG1Audio(track, func(pts time.Duration, frame []byte) {
				fail(writer.WriteMPEG1Audio(track, pts, frame))
			})
		case *codecs.AC3:
			reader.OnDataAC3(track, func(pts time.Duration, frame []byte) {
				fail(writer.WriteAC3(track, pts, frame))
			})
		case *codecs.G711:
			reader.OnDataG711(track, func(pts time.Duration, samples []byte) {
				fail(writer.WriteG711(track, pts, samples))
			})
		case *codecs.LPCM:
			reader.OnDataLPCM(track, func(pts time.Duration, samples []byte) {
				fail(writer.WriteLPCM(track, pts, samples))
			})
		default:
			return fmt.Errorf("unsupported codec %T", track.Codec)
		}
	}
	return nil
}
