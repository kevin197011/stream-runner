// Package main supervises ffmpeg processes that copy RTMP streams.
// Each configured stream is restarted until Stop. Disconnects are pulled again.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// ConfigPath is the default stream configuration file.
	ConfigPath = "/etc/stream-runner/streams.yml"
	// LogDir is the default log directory.
	LogDir = "/var/log/stream-runner"
	// LogFile is the default slog JSON log file.
	LogFile = "/var/log/stream-runner/stream.log"
	// PIDFilePath is the default pid file.
	PIDFilePath = "/var/run/stream-runner.pid"
	// MaxLogSize is the size that triggers log rotation (100MB).
	MaxLogSize = 100 * 1024 * 1024
	// MaxLogFiles is how many rotated logs to keep.
	MaxLogFiles = 5

	minRetry = time.Second
	maxRetry = 30 * time.Second
	// connectedMin stays above rwTimeout. A failed open dies at the read
	// timeout and must count as a short run so backoff keeps growing.
	connectedMin = 20 * time.Second
	rwTimeout    = 10 * time.Second
	statsPeriod  = 10 * time.Second
	// lagWarn is how far media time may fall behind wall time before a warning.
	// This is relay pace, not camera-to-screen latency.
	lagWarn     = 3 * time.Second
	dialTimeout = 2 * time.Second
	// netWarn is a slow TCP handshake to the pull source.
	netWarn = 300 * time.Millisecond
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

// StreamConfig is one RTMP copy.
type StreamConfig struct {
	ID  string `yaml:"id"`
	Src string `yaml:"src"`
	Dst string `yaml:"dst"`
}

// Config is the streams file.
type Config struct {
	Streams []StreamConfig `yaml:"streams"`
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

// AppState is the set of live workers.
type AppState struct {
	workers map[string]*StreamWorker
	mu      sync.Mutex
}

// StreamLogWriter prefixes each complete line with a timestamp and stream id.
type StreamLogWriter struct {
	streamID string
	writer   io.Writer
	buf      bytes.Buffer
	mu       sync.Mutex
}

// Write implements io.Writer. Incomplete lines stay buffered.
func (w *StreamLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return len(p), err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		if _, err := fmt.Fprintf(w.writer, "[%s] [%s] %s\n", timestamp, w.streamID, line); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
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

func rtmpAddr(src string) (string, error) {
	u, err := url.Parse(src)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("bad source url")
	}
	port := u.Port()
	if port == "" {
		port = "1935"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
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

func validateConfig(cfg *Config) error {
	seen := make(map[string]struct{}, len(cfg.Streams))
	for i, s := range cfg.Streams {
		if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Src) == "" || strings.TrimSpace(s.Dst) == "" {
			return fmt.Errorf("stream %d: id, src and dst are required", i)
		}
		if _, ok := seen[s.ID]; ok {
			return fmt.Errorf("duplicate stream id %q", s.ID)
		}
		seen[s.ID] = struct{}{}
	}
	return nil
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func writePID() {
	if err := os.MkdirAll("/var/run", 0755); err != nil {
		slog.Error("cannot create /var/run directory", "error", err)
		os.Exit(1)
	}
	f, err := os.OpenFile(PIDFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		slog.Error("cannot write pid file", "error", err)
		os.Exit(1)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			slog.Warn("failed to close pid file", "error", closeErr)
		}
		slog.Error("failed to write pid", "error", err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		slog.Warn("failed to close pid file", "error", err)
	}
}

func cleanupPID() {
	if err := os.Remove(PIDFilePath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove PID file", "error", err)
	}
}

var (
	logMu   sync.Mutex
	logFile *os.File
)

func shiftLogs(path string) error {
	for i := MaxLogFiles - 1; i >= 1; i-- {
		oldFile := fmt.Sprintf("%s.%d", path, i)
		newFile := fmt.Sprintf("%s.%d", path, i+1)
		if _, err := os.Stat(oldFile); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Rename(oldFile, newFile); err != nil {
			return fmt.Errorf("rename %s: %w", oldFile, err)
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

func rotateIfBig(path string, maxSize int64) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() < maxSize {
		return false, nil
	}
	if err := shiftLogs(path); err != nil {
		return false, err
	}
	return true, nil
}

func reopenAt(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	old := logFile
	logFile = f
	// ponytail: one global log file. Swap then close; an in-flight write during the hourly rotate can hit the closed fd.
	slog.SetDefault(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: true,
	})))
	if old != nil && old != f {
		if err := old.Close(); err != nil {
			slog.Warn("close rotated log", "error", err)
		}
	}
	return nil
}

func syncLog(path string, maxSize int64, forceOpen bool) error {
	logMu.Lock()
	defer logMu.Unlock()
	rotated, err := rotateIfBig(path, maxSize)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		forceOpen = true
	}
	if rotated || forceOpen {
		return reopenAt(path)
	}
	return nil
}

func initLog() error {
	if err := os.MkdirAll(LogDir, 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	return syncLog(LogFile, MaxLogSize, true)
}

func closeLog() {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		if err := logFile.Close(); err != nil {
			slog.Warn("close log file", "error", err)
		}
		logFile = nil
	}
}

func checkFFmpeg() error {
	cmd := exec.Command("ffmpeg", "-version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg not found or not executable: %v", err)
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		if _, err := fmt.Fprintf(os.Stderr, "[*] FFmpeg detected: %s\n", strings.TrimSpace(lines[0])); err != nil {
			slog.Warn("failed to write ffmpeg version to stderr", "error", err)
		}
	}
	return nil
}

// reloadConfig applies a valid config. A bad file returns before any worker changes.
func reloadConfig(state *AppState) error {
	cfg, err := loadConfig(ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	type change struct {
		id    string
		stop  *StreamWorker
		start *StreamWorker
	}
	var changes []change

	state.mu.Lock()
	keep := make(map[string]bool, len(cfg.Streams))
	for _, s := range cfg.Streams {
		if w, ok := state.workers[s.ID]; ok {
			keep[s.ID] = true
			if w.cfg.Src != s.Src || w.cfg.Dst != s.Dst {
				changes = append(changes, change{id: s.ID, stop: w, start: &StreamWorker{cfg: s}})
			}
			continue
		}
		changes = append(changes, change{id: s.ID, start: &StreamWorker{cfg: s}})
	}
	for id, w := range state.workers {
		if !keep[id] {
			changes = append(changes, change{id: id, stop: w})
		}
	}
	for _, c := range changes {
		if c.start != nil {
			state.workers[c.id] = c.start
		} else {
			delete(state.workers, c.id)
		}
	}
	state.mu.Unlock()

	for _, c := range changes {
		if c.stop != nil {
			slog.Info("stopping stream", "stream_id", c.id)
			c.stop.Stop()
		}
	}
	for _, c := range changes {
		if c.start != nil {
			slog.Info("starting stream", "stream_id", c.id)
			c.start.Start()
		}
	}
	return nil
}

func stopWorkers(state *AppState) {
	state.mu.Lock()
	workers := make([]*StreamWorker, 0, len(state.workers))
	for _, w := range state.workers {
		workers = append(workers, w)
	}
	state.mu.Unlock()
	for _, w := range workers {
		w.Stop()
	}
}

func run() int {
	if err := checkFFmpeg(); err != nil {
		if _, werr := fmt.Fprintf(os.Stderr, "ERROR: %v\n", err); werr != nil {
			return 1
		}
		return 1
	}
	if err := initLog(); err != nil {
		if _, werr := fmt.Fprintf(os.Stderr, "ERROR: %v\n", err); werr != nil {
			return 1
		}
		return 1
	}
	defer closeLog()

	writePID()
	defer cleanupPID()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	slog.Info("stream-runner starting")

	state := &AppState{workers: make(map[string]*StreamWorker)}
	if err := reloadConfig(state); err != nil {
		slog.Error("initial config load failed", "error", err)
		return 1
	}

	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := syncLog(LogFile, MaxLogSize, false); err != nil {
				slog.Error("log rotation failed", "error", err)
			}
		}
	}()

	for {
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			slog.Info("received SIGHUP, reloading config")
			if err := reloadConfig(state); err != nil {
				slog.Error("config reload failed", "error", err)
			} else {
				slog.Info("config reloaded")
			}
		case syscall.SIGINT, syscall.SIGTERM:
			slog.Info("shutting down")
			stopWorkers(state)
			return 0
		}
	}
}

func main() {
	os.Exit(run())
}
