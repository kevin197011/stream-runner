package main

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestWorkerRestartsUntilStop(t *testing.T) {
	orig := startFFmpeg
	var starts atomic.Int32
	startFFmpeg = func(src, dst string) *exec.Cmd {
		if starts.Add(1) == 1 {
			return exec.Command("sleep", "0.2")
		}
		return exec.Command("sleep", "30")
	}
	t.Cleanup(func() { startFFmpeg = orig })

	w := &StreamWorker{cfg: StreamConfig{ID: "t", Src: "a", Dst: "b"}}
	w.Start()
	deadline := time.Now().Add(4 * time.Second)
	for starts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if starts.Load() < 2 {
		t.Fatalf("ffmpeg was not restarted, starts=%d", starts.Load())
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

func TestProgressWarnsWhenSourceFallsBehind(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(20 * time.Second)}
	var i int
	now := func() time.Time {
		if i >= len(times) {
			return times[len(times)-1]
		}
		ts := times[i]
		i++
		return ts
	}
	in := strings.NewReader(strings.Join([]string{
		"out_time_us=0",
		"fps=25.0",
		"speed=1.0x",
		"progress=continue",
		"out_time_us=10000000",
		"fps=12.0",
		"speed=0.50x",
		"progress=continue",
		"",
	}, "\n"))
	readProgress("cam-1", in, now, func() pullNet {
		return pullNet{host: "src.example:1935", ip: "203.0.113.5", resolve: 4 * time.Millisecond, dial: 18 * time.Millisecond, resolved: true, dialed: true}
	})
	out := buf.String()
	if !strings.Contains(out, "source lagging") || !strings.Contains(out, "cam-1") || !strings.Contains(out, "behind_s=10.0") || !strings.Contains(out, "dial_ms=18") || !strings.Contains(out, "src_ip=203.0.113.5") || !strings.Contains(out, "resolve_ms=4") {
		t.Fatalf("log:\n%s", out)
	}
}

func TestPullNetworkWarnsWhenDialIsSlow(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	in := strings.NewReader("out_time_us=0\nfps=25.0\nspeed=1.0x\nprogress=continue\n")
	readProgress("cam-1", in, time.Now, func() pullNet {
		return pullNet{host: "src.example:1935", ip: "203.0.113.8", resolve: 5 * time.Millisecond, dial: netWarn, resolved: true, dialed: true}
	})
	out := buf.String()
	if !strings.Contains(out, "pull network") || !strings.Contains(out, "dial_ms=300") || !strings.Contains(out, "src_host=src.example:1935") {
		t.Fatalf("log:\n%s", out)
	}
}

func TestRTMPAddr(t *testing.T) {
	addr, err := rtmpAddr("rtmp://kkw-lb.example/tz32/8-321")
	if err != nil || addr != "kkw-lb.example:1935" {
		t.Fatalf("addr=%q err=%v", addr, err)
	}
}

func TestLogRotateReopensFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.log")
	prev := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		logMu.Lock()
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
		logMu.Unlock()
	})

	if err := syncLog(path, 32, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), 64), 0644); err != nil {
		t.Fatal(err)
	}
	if err := syncLog(path, 32, false); err != nil {
		t.Fatal(err)
	}
	slog.Info("after-rotate")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("after-rotate")) {
		t.Fatalf("new log missing marker: %s", body)
	}
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
