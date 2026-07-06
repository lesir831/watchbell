package checker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/watchbell/watchbell/internal/model"
)

type Checker interface {
	Type() string
	Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error)
}

type Registry map[string]Checker

func NewRegistry(checkers ...Checker) Registry {
	registry := make(Registry, len(checkers))
	for _, item := range checkers {
		registry[item.Type()] = item
	}
	return registry
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
