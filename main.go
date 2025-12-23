// Package main 提供 RTMP 流管理和转发服务。
// 支持多路流并发处理、自动重连、日志捕获和配置热重载。
package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// ConfigPath 是配置文件的默认路径。
	ConfigPath = "/etc/stream-runner/streams.yml"
	// LogDir 是日志文件的默认目录。
	LogDir = "/var/log/stream-runner"
	// LogFile 是主日志文件的默认路径。
	LogFile = "/var/log/stream-runner/stream.log"
	// PIDFilePath 是 PID 文件的默认路径。
	PIDFilePath = "/var/run/stream-runner.pid"
	// MaxLogSize 是日志文件的最大大小（100MB）。
	MaxLogSize = 100 * 1024 * 1024
	// MaxLogFiles 是保留的最大日志文件数量。
	MaxLogFiles = 5
)

// StreamConfig 表示单个 RTMP 流的配置信息。
type StreamConfig struct {
	// ID 是流的唯一标识符。
	ID string `yaml:"id"`
	// Src 是源 RTMP 流地址。
	Src string `yaml:"src"`
	// Dst 是目标 RTMP 流地址。
	Dst string `yaml:"dst"`
}

// Config 表示应用程序的完整配置。
type Config struct {
	// Streams 是所有要管理的 RTMP 流配置列表。
	Streams []StreamConfig `yaml:"streams"`
}

// StreamWorker 管理单个 RTMP 流的工作器，负责启动、监控和停止 ffmpeg 进程。
type StreamWorker struct {
	// cfg 是流的配置信息。
	cfg StreamConfig
	// running 表示工作器是否正在运行。
	running bool
	// cmd 是当前运行的 ffmpeg 命令进程。
	cmd *exec.Cmd
	// mu 保护并发访问的互斥锁。
	mu sync.Mutex
}

// AppState 表示应用程序的全局状态。
type AppState struct {
	// workers 是所有流工作器的映射表，key 为流 ID。
	workers map[string]*StreamWorker
	// mu 保护并发访问的读写互斥锁。
	mu sync.RWMutex
	// logger 是结构化日志记录器。
	logger *slog.Logger
}

// StreamLogWriter 包装 io.Writer，为每行日志添加流 ID 和时间戳前缀。
type StreamLogWriter struct {
	// streamID 是流的标识符，用于日志前缀。
	streamID string
	// writer 是底层写入器。
	writer io.Writer
	// buf 是缓冲区，用于处理不完整的行。
	buf bytes.Buffer
	// mu 保护并发写入的互斥锁。
	mu sync.Mutex
}

// Write 实现 io.Writer 接口，将数据写入并添加时间戳和流 ID 前缀。
func (w *StreamLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)

	// Process complete lines.
	for {
		line, err := w.buf.ReadString('\n')
		if err == io.EOF {
			break // Incomplete line, keep in buffer.
		}
		if err != nil {
			return len(p), err
		}

		// Remove trailing newline and write with prefix and timestamp.
		line = strings.TrimSuffix(line, "\n")
		if line != "" {
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			_, err = fmt.Fprintf(w.writer, "[%s] [%s] %s\n", timestamp, w.streamID, line)
			if err != nil {
				return len(p), err
			}
		}
	}

	return len(p), nil
}

// startLoop 启动流工作器的主循环，持续监控和重启 ffmpeg 进程。
func (w *StreamWorker) startLoop() {
	for {
		w.mu.Lock()
		w.running = true
		cmd := exec.Command("ffmpeg",
			"-rw_timeout", "2000000",
			"-i", w.cfg.Src,
			"-c", "copy",
			"-f", "flv",
			w.cfg.Dst,
		)

		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			w.mu.Unlock()
			slog.Error("failed to create stdout pipe", "stream_id", w.cfg.ID, "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			w.mu.Unlock()
			stdoutPipe.Close()
			slog.Error("failed to create stderr pipe", "stream_id", w.cfg.ID, "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		w.cmd = cmd
		w.mu.Unlock()

		slog.Info("starting ffmpeg", "stream_id", w.cfg.ID)
		if err := cmd.Start(); err != nil {
			slog.Error("failed to start ffmpeg", "stream_id", w.cfg.ID, "error", err)
			stdoutPipe.Close()
			stderrPipe.Close()
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
			time.Sleep(1 * time.Second)
			continue
		}

		// Create log writers to capture ffmpeg output.
		stdoutWriter := &StreamLogWriter{
			streamID: w.cfg.ID,
			writer:   os.Stdout,
		}
		stderrWriter := &StreamLogWriter{
			streamID: w.cfg.ID,
			writer:   os.Stderr,
		}

		// Start goroutines to continuously capture logs.
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			defer stdoutPipe.Close()
			io.Copy(stdoutWriter, stdoutPipe)
		}()

		go func() {
			defer wg.Done()
			defer stderrPipe.Close()
			io.Copy(stderrWriter, stderrPipe)
		}()

		err = cmd.Wait()
		wg.Wait() // Wait for log capture goroutines to finish.

		w.mu.Lock()
		w.running = false
		w.mu.Unlock()

		if err != nil {
			slog.Error("ffmpeg error", "stream_id", w.cfg.ID, "error", err)
		}
		slog.Info("stream ended, retry in 1s", "stream_id", w.cfg.ID)
		time.Sleep(1 * time.Second)
	}
}

// Start 启动流工作器，在独立的 goroutine 中运行。
func (w *StreamWorker) Start() { go w.startLoop() }

// IsRunning 检查流工作器是否正在运行。
func (w *StreamWorker) IsRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// ForceKill 强制终止流工作器及其关联的 ffmpeg 进程。
// 会先尝试终止整个进程组，如果失败则直接终止进程。
func (w *StreamWorker) ForceKill() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd == nil || w.cmd.Process == nil {
		w.running = false
		return
	}
	pid := w.cmd.Process.Pid
	slog.Info("force killing process", "stream_id", w.cfg.ID, "pid", pid)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		slog.Warn("kill failed, trying direct kill", "stream_id", w.cfg.ID, "error", err)
		syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = w.cmd.Wait()
	w.running = false
}

// loadConfig 从指定路径加载配置文件。
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// writePID 将当前进程的 PID 写入 PID 文件。
// 如果文件不存在会自动创建，如果写入失败会终止程序。
func writePID() {
	os.MkdirAll("/var/run", 0755)
	f, err := os.OpenFile(PIDFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		slog.Error("cannot write pid file", "error", err)
		os.Exit(1)
	}
	defer f.Close()
	fmt.Fprintf(f, "%d\n", os.Getpid())
}

// rotateLog 检查日志文件大小，如果超过限制则进行轮转。
// 轮转策略：将当前日志重命名为 .1，旧的 .1 重命名为 .2，以此类推。
func rotateLog() error {
	info, err := os.Stat(LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet, no need to rotate.
		}
		return err
	}

	if info.Size() < MaxLogSize {
		return nil // File is not large enough.
	}

	// Rotate existing logs.
	for i := MaxLogFiles - 1; i >= 1; i-- {
		oldFile := fmt.Sprintf("%s.%d", LogFile, i)
		newFile := fmt.Sprintf("%s.%d", LogFile, i+1)
		if _, err := os.Stat(oldFile); err == nil {
			os.Rename(oldFile, newFile)
		}
	}

	// Move current log to .1.
	backupFile := fmt.Sprintf("%s.1", LogFile)
	os.Rename(LogFile, backupFile)
	return nil
}

// initLog 初始化日志系统，创建日志目录和日志文件。
// 如果日志文件超过大小限制会先进行轮转。
// 如果初始化失败会 panic。
func initLog() *slog.Logger {
	os.MkdirAll(LogDir, 0755)

	// Rotate log if needed (before opening new file).
	_ = rotateLog()

	f, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		panic(fmt.Errorf("failed to open log file: %w", err))
	}

	// Create JSON format handler (recommended for production).
	opts := &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: true, // Add source code location.
	}
	handler := slog.NewJSONHandler(f, opts)
	logger := slog.New(handler)

	// Set as default logger.
	slog.SetDefault(logger)

	return logger
}

// cleanupPID 清理 PID 文件。
// 如果文件不存在则忽略错误。
func cleanupPID() {
	if err := os.Remove(PIDFilePath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove PID file", "error", err)
	}
}

// checkFFmpeg 检查系统中是否安装了 ffmpeg 并可以执行。
// 如果 ffmpeg 不可用则返回错误。
func checkFFmpeg() error {
	cmd := exec.Command("ffmpeg", "-version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg not found or not executable: %v", err)
	}

	// Extract version from output (first line usually contains version info).
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		fmt.Fprintf(os.Stderr, "[*] FFmpeg detected: %s\n", strings.TrimSpace(lines[0]))
	}
	return nil
}

// reloadConfig 重新加载配置文件并更新流工作器。
// 会停止已删除的流，启动新增的流，更新配置变更的流。
func reloadConfig(state *AppState) error {
	cfg, err := loadConfig(ConfigPath)
	if err != nil {
		return fmt.Errorf("load config failed: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	// Stop and remove workers that are no longer in config.
	for id, w := range state.workers {
		found := false
		for _, s := range cfg.Streams {
			if s.ID == id {
				found = true
				break
			}
		}
		if !found {
			slog.Info("removing worker", "stream_id", id)
			w.ForceKill()
			delete(state.workers, id)
		}
	}

	// Add or update workers.
	for _, s := range cfg.Streams {
		if w, exists := state.workers[s.ID]; exists {
			// Update config if changed.
			if w.cfg.Src != s.Src || w.cfg.Dst != s.Dst {
				slog.Info("updating worker", "stream_id", s.ID)
				w.ForceKill()
				w.cfg = s
				w.Start()
			}
		} else {
			// New worker.
			slog.Info("adding new worker", "stream_id", s.ID)
			w := &StreamWorker{cfg: s}
			state.workers[s.ID] = w
			w.Start()
		}
	}

	return nil
}

func main() {
	// Check ffmpeg availability before starting.
	if err := checkFFmpeg(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	logger := initLog()
	defer func() {
		// Logger will handle file closing when done.
		_ = logger
	}()

	writePID()
	defer cleanupPID()

	// Setup signal handlers.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	slog.Info("stream-runner starting")

	state := &AppState{
		workers: make(map[string]*StreamWorker),
		logger:  logger,
	}

	// Initial config load.
	if err := reloadConfig(state); err != nil {
		slog.Error("initial config load failed", "error", err)
		os.Exit(1)
	}

	// Watchdog goroutine monitors and restarts stopped workers.
	go func() {
		time.Sleep(10 * time.Second) // Give workers time to start.
		for {
			time.Sleep(5 * time.Second)
			state.mu.RLock()
			for id, w := range state.workers {
				if !w.IsRunning() {
					slog.Warn("worker not running, force kill & restart", "stream_id", id)
					w.ForceKill()
					time.Sleep(1 * time.Second) // Wait before next check.
				}
			}
			state.mu.RUnlock()
		}
	}()

	// Log rotation checker runs periodically.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := rotateLog(); err != nil {
				slog.Error("log rotation check failed", "error", err)
			} else {
				// Check if rotation actually happened (file was renamed).
				if info, err := os.Stat(LogFile); err == nil && info.Size() == 0 {
					// File was rotated, reopen it.
					newFile, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
					if err == nil {
						opts := &slog.HandlerOptions{
							Level:     slog.LevelInfo,
							AddSource: true,
						}
						handler := slog.NewJSONHandler(newFile, opts)
						state.mu.Lock()
						state.logger = slog.New(handler)
						slog.SetDefault(state.logger)
						state.mu.Unlock()
					}
				}
			}
		}
	}()

	// Main signal loop handles SIGHUP (reload) and SIGINT/SIGTERM (shutdown).
	for {
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			slog.Info("received SIGHUP, reloading config")
			if err := reloadConfig(state); err != nil {
				slog.Error("config reload failed", "error", err)
			} else {
				slog.Info("config reloaded successfully")
			}
		case syscall.SIGINT, syscall.SIGTERM:
			slog.Info("received termination signal, shutting down")
			state.mu.Lock()
			for id, w := range state.workers {
				slog.Info("stopping worker", "stream_id", id)
				w.ForceKill()
			}
			state.mu.Unlock()
			cleanupPID()
			os.Exit(0)
		}
	}
}
