// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// LogDir is the default log directory.
	LogDir = "/var/log/stream-runner"
	// LogFile is the default slog JSON log file.
	LogFile = "/var/log/stream-runner/stream.log"
	// LogKeepDays is how many calendar days of rotated logs to keep (about one month).
	LogKeepDays = 30
)

// logNow is the clock for day boundaries; tests replace it.
var logNow = time.Now

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

var (
	logMu   sync.Mutex
	logFile *os.File
)

func localDay(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02")
}

// rotateIfNewDay renames path to path.YYYY-MM-DD when its local mod-day is before today.
func rotateIfNewDay(path string, now time.Time) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fileDay := localDay(info.ModTime())
	if fileDay == localDay(now) {
		return false, nil
	}
	dest := path + "." + fileDay
	if _, err := os.Stat(dest); err == nil {
		if err := os.Remove(dest); err != nil {
			return false, fmt.Errorf("remove existing %s: %w", dest, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Rename(path, dest); err != nil {
		return false, fmt.Errorf("rename %s: %w", path, err)
	}
	return true, nil
}

// purgeOldLogs deletes dated rotates older than keepDays and legacy size-based .N files.
func purgeOldLogs(path string, keepDays int, now time.Time) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	prefix := base + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := now.In(time.Local).AddDate(0, 0, -keepDays)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		full := filepath.Join(dir, name)
		if _, err := strconv.Atoi(suffix); err == nil {
			// Legacy stream.log.1 … stream.log.5 from size rotation.
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", suffix, time.Local)
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func reopenAt(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	old := logFile
	logFile = f
	// ponytail: one global log file. Swap then close; an in-flight write during rotate can hit the closed fd.
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

// SyncLog rotates on local calendar day change, purges old dated files, and may reopen.
func SyncLog(path string, forceOpen bool) error {
	logMu.Lock()
	defer logMu.Unlock()
	now := logNow()
	rotated, err := rotateIfNewDay(path, now)
	if err != nil {
		return err
	}
	if err := purgeOldLogs(path, LogKeepDays, now); err != nil {
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

// InitLog creates the log directory and opens the JSON log file.
func InitLog() error {
	if err := os.MkdirAll(LogDir, 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	return SyncLog(LogFile, true)
}

// CloseLog closes the slog file handle.
func CloseLog() {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		if err := logFile.Close(); err != nil {
			slog.Warn("close log file", "error", err)
		}
		logFile = nil
	}
}
