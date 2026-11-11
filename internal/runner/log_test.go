// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogRotateByDay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.log")
	prev := slog.Default()
	prevNow := logNow
	t.Cleanup(func() {
		slog.SetDefault(prev)
		logNow = prevNow
		logMu.Lock()
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
		logMu.Unlock()
	})

	yesterday := time.Date(2026, 11, 10, 15, 0, 0, 0, time.Local)
	today := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	logNow = func() time.Time { return yesterday }

	if err := SyncLog(path, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"msg":"old-day"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}

	logNow = func() time.Time { return today }
	if err := SyncLog(path, false); err != nil {
		t.Fatal(err)
	}
	archived := path + ".2026-11-10"
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("expected dated archive %s: %v", archived, err)
	}
	slog.Info("after-rotate")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("after-rotate")) {
		t.Fatalf("new log missing marker: %s", body)
	}
	oldBody, err := os.ReadFile(archived)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(oldBody, []byte("old-day")) {
		t.Fatalf("archive missing old content: %s", oldBody)
	}
}

func TestLogPurgeOlderThanKeepDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.log")
	prev := slog.Default()
	prevNow := logNow
	t.Cleanup(func() {
		slog.SetDefault(prev)
		logNow = prevNow
		logMu.Lock()
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
		logMu.Unlock()
	})

	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local)
	logNow = func() time.Time { return now }

	keep := path + ".2026-10-20"
	drop := path + ".2026-10-01"
	legacy := path + ".3"
	for _, p := range []string{keep, drop, legacy} {
		if err := os.WriteFile(p, []byte("x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := SyncLog(path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("recent archive should remain: %v", err)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Fatalf("old archive should be removed, err=%v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy numbered log should be removed, err=%v", err)
	}
}

func TestStreamLogWriter(t *testing.T) {
	var buf bytes.Buffer
	writer := &StreamLogWriter{
		streamID: "test-stream",
		writer:   &buf,
	}

	testData := []byte("test log line\nanother line\n")
	n, err := writer.Write(testData)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if n != len(testData) {
		t.Errorf("expected to write %d bytes, got %d", len(testData), n)
	}

	output := buf.String()
	if output == "" {
		t.Error("expected output to contain log lines")
	}

	if !strings.Contains(output, "test-stream") {
		t.Error("expected output to contain stream ID")
	}
}
