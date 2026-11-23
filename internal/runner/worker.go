// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	minRetry = time.Second
	maxRetry = 30 * time.Second
	// connectedMin stays above rwTimeout. A failed open dies at the read
	// timeout and must count as a short run so backoff keeps growing.
	connectedMin = 20 * time.Second
	rwTimeout    = 10 * time.Second
)

// StreamWorker runs one RTMP remux until Stop.
// The loop exits only when stop is closed. relay errors retry.
type StreamWorker struct {
	cfg     StreamConfig
	cancel  context.CancelFunc
	stop    chan struct{}
	stopped chan struct{}
	mu      sync.Mutex
}

func retryAfter(ran, current time.Duration) time.Duration {
	if ran >= connectedMin {
		return minRetry
	}
	next := current * 2
	if next > maxRetry || next < current {
		return maxRetry
	}
	return next
}

func (w *StreamWorker) stoppedRequested() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

// sleepOrStop waits for d. It returns false when Stop was requested.
func (w *StreamWorker) sleepOrStop(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-w.stop:
		return false
	case <-timer.C:
		return true
	}
}

// runOnce starts one remux and waits for it. The caller retries unless Stop was requested.
func (w *StreamWorker) runOnce() (time.Duration, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.mu.Lock()
	if w.stoppedRequested() {
		w.mu.Unlock()
		return 0, nil
	}
	w.cancel = cancel
	w.mu.Unlock()

	go func() {
		select {
		case <-w.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	slog.Info("starting relay", "stream_id", w.cfg.ID)
	started := time.Now()
	setStreamUp(w.cfg.ID, true)
	err := relayOnce(ctx, w.cfg.ID, w.cfg.Src, w.cfg.Dst)
	setStreamUp(w.cfg.ID, false)

	w.mu.Lock()
	w.cancel = nil
	w.mu.Unlock()
	return time.Since(started), err
}

// startLoop pulls the stream again after every relay exit until Stop.
func (w *StreamWorker) startLoop() {
	defer close(w.stopped)
	backoff := minRetry
	for {
		if w.stoppedRequested() {
			return
		}
		ran, err := w.runOnce()
		if w.stoppedRequested() {
			return
		}
		incRelayRestarts(w.cfg.ID)
		exitAttrs := []any{"stream_id", w.cfg.ID, "retry_in", backoff.String()}
		exitAttrs = append(exitAttrs, probePull(w.cfg.Src).logAttrs()...)
		if err != nil {
			slog.Error("relay exited", append([]any{"error", err}, exitAttrs...)...)
		} else {
			slog.Info("relay exited", exitAttrs...)
		}
		if !w.sleepOrStop(backoff) {
			return
		}
		backoff = retryAfter(ran, backoff)
	}
}

// Start begins the retry loop.
func (w *StreamWorker) Start() {
	w.mu.Lock()
	w.stop = make(chan struct{})
	w.stopped = make(chan struct{})
	w.mu.Unlock()
	go w.startLoop()
}

// Stop ends the retry loop and cancels the active relay.
func (w *StreamWorker) Stop() {
	w.mu.Lock()
	if w.stop == nil {
		w.mu.Unlock()
		return
	}
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	cancel := w.cancel
	stopped := w.stopped
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stopped != nil {
		<-stopped
	}
}
