package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestStreamConfig 测试 StreamConfig 结构体
func TestStreamConfig(t *testing.T) {
	cfg := StreamConfig{
		ID:  "test-stream",
		Src: "rtmp://source.com/live/stream",
		Dst: "rtmp://dest.com/live/stream",
	}

	if cfg.ID != "test-stream" {
		t.Errorf("expected ID to be 'test-stream', got %s", cfg.ID)
	}
	if cfg.Src == "" {
		t.Error("Src should not be empty")
	}
	if cfg.Dst == "" {
		t.Error("Dst should not be empty")
	}
}

// TestConfig 测试 Config 结构体
func TestConfig(t *testing.T) {
	cfg := Config{
		Streams: []StreamConfig{
			{ID: "stream-1", Src: "rtmp://src1.com/live", Dst: "rtmp://dst1.com/live"},
			{ID: "stream-2", Src: "rtmp://src2.com/live", Dst: "rtmp://dst2.com/live"},
		},
	}

	if len(cfg.Streams) != 2 {
		t.Errorf("expected 2 streams, got %d", len(cfg.Streams))
	}
}

// TestLoadConfig 测试配置文件加载
func TestLoadConfig(t *testing.T) {
	// 创建临时配置文件
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

// TestLoadConfigInvalidPath 测试加载不存在的配置文件
func TestLoadConfigInvalidPath(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent config file")
	}
}

// TestLoadConfigInvalidYAML 测试加载无效的 YAML 文件
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

// TestStreamWorkerIsRunning 测试 StreamWorker 的 IsRunning 方法
func TestStreamWorkerIsRunning(t *testing.T) {
	worker := &StreamWorker{
		cfg: StreamConfig{
			ID:  "test-stream",
			Src: "rtmp://source.com/live",
			Dst: "rtmp://dest.com/live",
		},
		running: false,
	}

	if worker.IsRunning() {
		t.Error("expected worker to not be running initially")
	}
}

// TestRotateLog 测试日志轮转功能
func TestRotateLog(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	// 创建一个大文件（模拟需要轮转的情况）
	largeContent := make([]byte, MaxLogSize+1)
	for i := range largeContent {
		largeContent[i] = 'a'
	}

	if err := os.WriteFile(logFile, largeContent, 0644); err != nil {
		t.Fatalf("failed to create test log file: %v", err)
	}

	// 临时修改 LogFile 常量（通过环境变量或函数参数）
	// 由于 LogFile 是常量，我们需要创建一个测试函数
	originalLogFile := LogFile
	defer func() {
		// 恢复原始值（虽然常量不能修改，但这里只是演示测试思路）
		_ = originalLogFile
	}()

	// 注意：由于 rotateLog 使用全局常量 LogFile，这个测试需要重构代码
	// 或者创建一个接受路径参数的版本
	// 这里仅作为测试示例
}

// TestStreamLogWriter 测试 StreamLogWriter
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

	// 检查是否包含时间戳和流 ID
	if !strings.Contains(output, "test-stream") {
		t.Error("expected output to contain stream ID")
	}
}

// TestYAMLUnmarshal 测试 YAML 解析
func TestYAMLUnmarshal(t *testing.T) {
	yamlContent := `streams:
  - id: stream-1
    src: rtmp://source.com/live/stream1
    dst: rtmp://dest.com/live/stream1
  - id: stream-2
    src: rtmp://source.com/live/stream2
    dst: rtmp://dest.com/live/stream2
`

	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	if len(cfg.Streams) != 2 {
		t.Errorf("expected 2 streams, got %d", len(cfg.Streams))
	}
}

// BenchmarkLoadConfig 基准测试配置文件加载
func BenchmarkLoadConfig(b *testing.B) {
	tmpDir := b.TempDir()
	configPath := filepath.Join(tmpDir, "bench-config.yaml")

	configContent := `streams:
  - id: test-stream
    src: rtmp://source.com/live/stream
    dst: rtmp://dest.com/live/stream
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		b.Fatalf("failed to create test config file: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := loadConfig(configPath)
		if err != nil {
			b.Fatalf("loadConfig failed: %v", err)
		}
	}
}
