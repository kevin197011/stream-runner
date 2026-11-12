// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
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

// startFFmpeg builds the copy process for one stream. Tests replace it.
var startFFmpeg = func(src, dst string) *exec.Cmd {
	return exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "warning",
		"-stats_period", strconv.Itoa(int(statsPeriod.Seconds())),
		"-progress", "pipe:3",
		"-rw_timeout", fmt.Sprintf("%d", rwTimeout.Microseconds()),
		"-rtmp_live", "live",
		"-i", src,
		"-c", "copy",
		"-f", "flv",
		dst,
	)
}

// StreamWorker runs one ffmpeg copy until Stop.
// The loop exits only when stop is closed. ffmpeg errors retry.
type StreamWorker struct {
	cfg     StreamConfig
	cmd     *exec.Cmd
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

func copyPipe(streamID string, pipe io.ReadCloser, dst io.Writer) {
	defer func() {
		if err := pipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.Warn("close ffmpeg pipe", "stream_id", streamID, "error", err)
		}
	}()
	writer := &StreamLogWriter{streamID: streamID, writer: dst}
	if _, err := io.Copy(writer, pipe); err != nil {
		slog.Warn("copy ffmpeg output", "stream_id", streamID, "error", err)
	}
}

func closeRead(c io.Closer) {
	if err := c.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		slog.Warn("close pipe", "error", err)
	}
}

// runOnce starts ffmpeg and waits for it. The caller retries unless Stop was requested.
func (w *StreamWorker) runOnce() (time.Duration, error) {
	cmd := startFFmpeg(w.cfg.Src, w.cfg.Dst)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		closeRead(stdoutPipe)
		return 0, err
	}
	progressR, progressW, err := os.Pipe()
	if err != nil {
		closeRead(stdoutPipe)
		closeRead(stderrPipe)
		return 0, err
	}
	cmd.ExtraFiles = []*os.File{progressW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	w.mu.Lock()
	if w.stoppedRequested() {
		w.mu.Unlock()
		closeRead(stdoutPipe)
		closeRead(stderrPipe)
		closeRead(progressR)
		closeRead(progressW)
		return 0, nil
	}
	w.cmd = cmd
	w.mu.Unlock()

	slog.Info("starting ffmpeg", "stream_id", w.cfg.ID)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		closeRead(stdoutPipe)
		closeRead(stderrPipe)
		closeRead(progressR)
		closeRead(progressW)
		w.mu.Lock()
		w.cmd = nil
		w.mu.Unlock()
		return 0, err
	}
	setStreamUp(w.cfg.ID, true)
	closeRead(progressW)
	if w.stoppedRequested() {
		w.kill()
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		copyPipe(w.cfg.ID, stdoutPipe, os.Stdout)
	}()
	go func() {
		defer wg.Done()
		copyPipe(w.cfg.ID, stderrPipe, os.Stderr)
	}()
	go func() {
		defer wg.Done()
		defer closeRead(progressR)
		readProgress(w.cfg.ID, progressR, time.Now, func() pullNet {
			return probePull(w.cfg.Src)
		})
	}()

	err = cmd.Wait()
	wg.Wait()

	setStreamUp(w.cfg.ID, false)
	w.mu.Lock()
	w.cmd = nil
	w.mu.Unlock()
	return time.Since(started), err
}

// startLoop pulls the stream again after every ffmpeg exit until Stop.
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
		incFFmpegRestarts(w.cfg.ID)
		exitAttrs := []any{"stream_id", w.cfg.ID, "retry_in", backoff.String()}
		exitAttrs = append(exitAttrs, probePull(w.cfg.Src).logAttrs()...)
		if err != nil {
			slog.Error("ffmpeg exited", append([]any{"error", err}, exitAttrs...)...)
		} else {
			slog.Info("ffmpeg exited", exitAttrs...)
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

func (w *StreamWorker) kill() {
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	slog.Info("killing ffmpeg", "stream_id", w.cfg.ID, "pid", pid)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Warn("kill process group failed", "stream_id", w.cfg.ID, "error", err)
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			slog.Warn("kill process failed", "stream_id", w.cfg.ID, "error", err)
		}
	}
}

// Stop ends the retry loop and kills ffmpeg. It does not wait on the process;
// startLoop owns Cmd.Wait.
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
	w.mu.Unlock()
	w.kill()
	<-w.stopped
}
