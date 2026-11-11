// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.yaml")

	configContent := `streams:
  - id: test-stream-1
    src: rtmp://source.com/live/stream1
    dst: rtmp://dest.com/live/stream1
  - id: test-stream-2
    src: rtmp://source.com/live/stream2
    dst: rtmp://dest.com/live/stream2
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to create test config file: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if len(cfg.Streams) != 2 {
		t.Errorf("expected 2 streams, got %d", len(cfg.Streams))
	}

	if cfg.Streams[0].ID != "test-stream-1" {
		t.Errorf("expected first stream ID to be 'test-stream-1', got %s", cfg.Streams[0].ID)
	}
}

func TestLoadConfigInvalidPath(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent config file")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid-config.yaml")

	invalidYAML := `streams:
  - id: test-stream
    src: rtmp://source.com/live
    dst: [invalid yaml
`

	if err := os.WriteFile(configPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("failed to create invalid config file: %v", err)
	}

	_, err := loadConfig(configPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadConfigRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "streams.yml")
	body := `streams:
  - id: same
    src: rtmp://a/live
    dst: rtmp://b/live
  - id: same
    src: rtmp://c/live
    dst: rtmp://d/live
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected duplicate id to fail")
	}
}
