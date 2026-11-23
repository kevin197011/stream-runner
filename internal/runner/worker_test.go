// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	if retryAfter(connectedMin, 16*time.Second) != minRetry {
		t.Fatal("a run that reached connectedMin should reset the delay")
	}
	if retryAfter(rwTimeout, time.Second) != 2*time.Second {
		t.Fatal("a failed open at the read timeout should double the delay")
	}
	if retryAfter(time.Second, maxRetry) != maxRetry {
		t.Fatal("delay should cap")
	}
}

func TestWorkerRestartsUntilStop(t *testing.T) {
	orig := relayOnce
	var starts atomic.Int32
	relayOnce = func(ctx context.Context, id, src, dst string) error {
		n := starts.Add(1)
		if n == 1 {
			return context.DeadlineExceeded
		}
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { relayOnce = orig })

	w := &StreamWorker{cfg: StreamConfig{ID: "t", Src: "rtmp://a/live/x", Dst: "rtmp://b/live/x"}}
	w.Start()
	deadline := time.Now().Add(4 * time.Second)
	for starts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if starts.Load() < 2 {
		t.Fatalf("relay was not restarted, starts=%d", starts.Load())
	}

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung")
	}
	got := starts.Load()
	time.Sleep(1500 * time.Millisecond)
	if starts.Load() != got {
		t.Fatalf("worker restarted after Stop: %d -> %d", got, starts.Load())
	}
}
