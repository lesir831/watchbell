package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/watchbell/watchbell/internal/model"
)

type Checker interface {
	Type() string
	Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error)
}

type DescribedChecker interface {
	Checker
	Plugin() model.MonitorPlugin
}

type Registry map[string]Checker

func NewRegistry(checkers ...Checker) Registry {
	registry := make(Registry, len(checkers))
	for _, item := range checkers {
		registry[item.Type()] = item
	}
	return registry
}

func (r Registry) Has(pluginID string) bool {
	_, ok := r[pluginID]
	return ok
}

func (r Registry) Plugins() []model.MonitorPlugin {
	plugins := make([]model.MonitorPlugin, 0, len(r))
	for pluginID, item := range r {
		if described, ok := item.(DescribedChecker); ok {
			plugins = append(plugins, described.Plugin())
			continue
		}
		plugins = append(plugins, model.MonitorPlugin{
			ID: pluginID, Name: pluginID, Builtin: true,
			DefaultIntervalSeconds: 300,
			DefaultConfig:          map[string]any{},
		})
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Name < plugins[j].Name
	})
	return plugins
}

func DecodeConfig[T any](monitor model.Monitor, fallback T) (T, error) {
	if len(monitor.Config) == 0 {
		return fallback, nil
	}
	if err := json.Unmarshal(monitor.Config, &fallback); err != nil {
		return fallback, fmt.Errorf("decode %s config: %w", monitor.Type, err)
	}
	return fallback, nil
}

func DecodeState[T any](monitor model.Monitor, fallback T) T {
	if len(monitor.State) == 0 {
		return fallback
	}
	_ = json.Unmarshal(monitor.State, &fallback)
	return fallback
}
