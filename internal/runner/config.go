// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// ConfigPath is the default stream configuration file.
	ConfigPath = "/etc/stream-runner/streams.yml"
	// PIDFilePath is the default pid file.
	PIDFilePath = "/var/run/stream-runner.pid"
)

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
