package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
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
	ConfigPath  = "/etc/stream-runner/streams.yaml"
	LogDir      = "/var/log/stream-runner"
	LogFile     = "/var/log/stream-runner/stream.log"
	PIDFilePath = "/var/run/stream-runner.pid"
	MaxLogSize  = 100 * 1024 * 1024 // 100MB
	MaxLogFiles = 5
)

type StreamConfig struct {
	ID  string `yaml:"id"`
	Src string `yaml:"src"`
	Dst string `yaml:"dst"`
}

type Config struct {
	Streams []StreamConfig `yaml:"streams"`
}

type StreamWorker struct {
	cfg     StreamConfig
	running bool
	cmd     *exec.Cmd
	mu      sync.Mutex
}

type AppState struct {
	workers map[string]*StreamWorker
	mu      sync.RWMutex
	logFile *os.File
}

// StreamLogWriter wraps an io.Writer and prefixes each line with stream ID
type StreamLogWriter struct {
	streamID string
	writer   io.Writer
	buf      bytes.Buffer
	mu       sync.Mutex
}

func (w *StreamLogWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Write all input to buffer
	w.buf.Write(p)

	// Process complete lines
	for {
		line, err := w.buf.ReadString('\n')
		if err == io.EOF {
			break // Incomplete line, keep in buffer
		}
		if err != nil {
			return len(p), err
		}

		// Remove trailing newline and write with prefix and timestamp
		line = strings.TrimSuffix(line, "\n")
		if line != "" {
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			_, err = fmt.Fprintf(w.writer, "[%s] [%s] %s\n", timestamp, w.streamID, line)
			if err != nil {
				return len(p), err
			}
		}
	}

	// Return the number of bytes we accepted (all of them)
	return len(p), nil
}

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

		// Create pipes for stdout and stderr
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			w.mu.Unlock()
			log.Printf("[%s] failed to create stdout pipe: %v", w.cfg.ID, err)
			time.Sleep(1 * time.Second)
			continue
		}

		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			w.mu.Unlock()
			stdoutPipe.Close()
			log.Printf("[%s] failed to create stderr pipe: %v", w.cfg.ID, err)
			time.Sleep(1 * time.Second)
			continue
		}

		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		w.cmd = cmd
		w.mu.Unlock()

		// Start the command
		log.Printf("[%s] starting ffmpeg...", w.cfg.ID)
		if err := cmd.Start(); err != nil {
			log.Printf("[%s] failed to start ffmpeg: %v", w.cfg.ID, err)
			stdoutPipe.Close()
			stderrPipe.Close()
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
			time.Sleep(1 * time.Second)
			continue
		}

		// Create log writers
		stdoutWriter := &StreamLogWriter{
			streamID: w.cfg.ID,
			writer:   log.Writer(),
		}
		stderrWriter := &StreamLogWriter{
			streamID: w.cfg.ID,
			writer:   log.Writer(),
		}

		// Start goroutines to continuously capture logs
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

		// Wait for command to finish
		err = cmd.Wait()
		wg.Wait() // Wait for log capture goroutines to finish

		w.mu.Lock()
		w.running = false
		w.mu.Unlock()

		if err != nil {
			log.Printf("[%s] ffmpeg error: %v", w.cfg.ID, err)
		}
		log.Printf("[%s] stream ended, retry in 1s ...", w.cfg.ID)
		time.Sleep(1 * time.Second)
	}
}

func (w *StreamWorker) Start() { go w.startLoop() }
func (w *StreamWorker) IsRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}
func (w *StreamWorker) ForceKill() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd == nil || w.cmd.Process == nil {
		w.running = false
		return
	}
	pid := w.cmd.Process.Pid
	log.Printf("[%s] force killing pid=%d", w.cfg.ID, pid)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		log.Printf("[%s] kill failed: %v, trying direct kill", w.cfg.ID, err)
		syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = w.cmd.Wait()
	w.running = false
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
	return &cfg, nil
}

func writePID() {
	os.MkdirAll("/var/run", 0755)
	f, err := os.OpenFile(PIDFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatalf("cannot write pid: %v", err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%d\n", os.Getpid())
}

func rotateLog() error {
	// Check if log file needs rotation
	info, err := os.Stat(LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet, no need to rotate
		}
		return err
	}

	if info.Size() < MaxLogSize {
		return nil // File is not large enough
	}

	// Rotate existing logs
	for i := MaxLogFiles - 1; i >= 1; i-- {
		oldFile := fmt.Sprintf("%s.%d", LogFile, i)
		newFile := fmt.Sprintf("%s.%d", LogFile, i+1)
		if _, err := os.Stat(oldFile); err == nil {
			os.Rename(oldFile, newFile)
		}
	}

	// Move current log to .1
	backupFile := fmt.Sprintf("%s.1", LogFile)
	os.Rename(LogFile, backupFile)
	return nil
}

func initLog() *os.File {
	os.MkdirAll(LogDir, 0755)

	// Rotate log if needed (before opening new file)
	_ = rotateLog()

	f, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	return f
}

func cleanupPID() {
	if err := os.Remove(PIDFilePath); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: failed to remove PID file: %v", err)
	}
}

func checkFFmpeg() error {
	cmd := exec.Command("ffmpeg", "-version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg not found or not executable: %v", err)
	}

	// Extract version from output (first line usually contains version info)
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		fmt.Fprintf(os.Stderr, "[*] FFmpeg detected: %s\n", strings.TrimSpace(lines[0]))
	}
	return nil
}

func reloadConfig(state *AppState) error {
	cfg, err := loadConfig(ConfigPath)
	if err != nil {
		return fmt.Errorf("load config failed: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	// Stop and remove workers that are no longer in config
	for id, w := range state.workers {
		found := false
		for _, s := range cfg.Streams {
			if s.ID == id {
				found = true
				break
			}
		}
		if !found {
			log.Printf("[Reload] Removing worker: %s", id)
			w.ForceKill()
			delete(state.workers, id)
		}
	}

	// Add or update workers
	for _, s := range cfg.Streams {
		if w, exists := state.workers[s.ID]; exists {
			// Update config if changed
			if w.cfg.Src != s.Src || w.cfg.Dst != s.Dst {
				log.Printf("[Reload] Updating worker: %s", s.ID)
				w.ForceKill()
				w.cfg = s
				w.Start()
			}
		} else {
			// New worker
			log.Printf("[Reload] Adding new worker: %s", s.ID)
			w := &StreamWorker{cfg: s}
			state.workers[s.ID] = w
			w.Start()
		}
	}

	return nil
}

func main() {
	// Check ffmpeg availability before starting
	if err := checkFFmpeg(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	logFile := initLog()
	defer logFile.Close()

	writePID()
	defer cleanupPID()

	// Setup signal handlers
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	log.Println("==== stream-runner starting ====")

	state := &AppState{
		workers: make(map[string]*StreamWorker),
		logFile: logFile,
	}

	// Initial config load
	if err := reloadConfig(state); err != nil {
		log.Fatalf("Initial config load failed: %v", err)
	}

	// Watchdog goroutine
	go func() {
		time.Sleep(10 * time.Second) // Give workers time to start
		for {
			time.Sleep(5 * time.Second)
			state.mu.RLock()
			for id, w := range state.workers {
				if !w.IsRunning() {
					log.Printf("[Watchdog] %s not running, force kill & restart", id)
					w.ForceKill()
					time.Sleep(1 * time.Second) // Wait before next check
				}
			}
			state.mu.RUnlock()
		}
	}()

	// Log rotation checker
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := rotateLog(); err != nil {
				log.Printf("Log rotation check failed: %v", err)
			} else {
				// Check if rotation actually happened (file was renamed)
				if info, err := os.Stat(LogFile); err == nil && info.Size() == 0 {
					// File was rotated, reopen it
					state.mu.Lock()
					if state.logFile != nil {
						state.logFile.Close()
					}
					newFile, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
					if err == nil {
						state.logFile = newFile
						log.SetOutput(newFile)
					}
					state.mu.Unlock()
				}
			}
		}
	}()

	// Main signal loop
	for {
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			log.Println("Received SIGHUP, reloading config...")
			if err := reloadConfig(state); err != nil {
				log.Printf("Config reload failed: %v", err)
			} else {
				log.Println("Config reloaded successfully")
			}
		case syscall.SIGINT, syscall.SIGTERM:
			log.Println("Received termination signal, shutting down...")
			state.mu.Lock()
			for id, w := range state.workers {
				log.Printf("Stopping worker: %s", id)
				w.ForceKill()
			}
			state.mu.Unlock()
			cleanupPID()
			os.Exit(0)
		}
	}
}
