// Copyright (c) 2026 kk
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package runner

import (
	"fmt"
	"log/slog"
	"sync"
)

// AppState is the set of live workers.
type AppState struct {
	workers map[string]*StreamWorker
	mu      sync.Mutex
}

// NewAppState returns an empty worker map.
func NewAppState() *AppState {
	return &AppState{workers: make(map[string]*StreamWorker)}
}

// ReloadConfig applies a valid config. A bad file returns before any worker changes.
func ReloadConfig(state *AppState) error {
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

// StopWorkers stops every live worker.
func StopWorkers(state *AppState) {
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
