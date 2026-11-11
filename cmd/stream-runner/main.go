// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package main supervises ffmpeg processes that copy RTMP streams.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kevin197011/stream-runner/internal/runner"
)

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

func writePID() {
	if err := os.MkdirAll("/var/run", 0755); err != nil {
		slog.Error("cannot create /var/run directory", "error", err)
		os.Exit(1)
	}
	f, err := os.OpenFile(runner.PIDFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
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
	if err := os.Remove(runner.PIDFilePath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove PID file", "error", err)
	}
}

func run() int {
	if err := checkFFmpeg(); err != nil {
		if _, werr := fmt.Fprintf(os.Stderr, "ERROR: %v\n", err); werr != nil {
			return 1
		}
		return 1
	}
	if err := runner.InitLog(); err != nil {
		if _, werr := fmt.Fprintf(os.Stderr, "ERROR: %v\n", err); werr != nil {
			return 1
		}
		return 1
	}
	defer runner.CloseLog()

	writePID()
	defer cleanupPID()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	slog.Info("stream-runner starting")

	state := runner.NewAppState()
	if err := runner.ReloadConfig(state); err != nil {
		slog.Error("initial config load failed", "error", err)
		return 1
	}

	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := runner.SyncLog(runner.LogFile, false); err != nil {
				slog.Error("log rotation failed", "error", err)
			}
		}
	}()

	for {
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			slog.Info("received SIGHUP, reloading config")
			if err := runner.ReloadConfig(state); err != nil {
				slog.Error("config reload failed", "error", err)
			} else {
				slog.Info("config reloaded")
			}
		case syscall.SIGINT, syscall.SIGTERM:
			slog.Info("shutting down")
			runner.StopWorkers(state)
			return 0
		}
	}
}

func main() {
	os.Exit(run())
}
