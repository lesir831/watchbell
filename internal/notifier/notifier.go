package notifier

import (
	"context"

	"github.com/watchbell/watchbell/internal/model"
)

type Message struct {
	Subject string
	Body    string
	Data    map[string]any
}

type Notifier interface {
	Type() string
	Send(ctx context.Context, channel model.NotifyChannel, message Message) error
}

type Registry map[string]Notifier

func NewRegistry(notifiers ...Notifier) Registry {
	registry := make(Registry, len(notifiers))
	for _, item := range notifiers {
		registry[item.Type()] = item
	}
	return registry
}
