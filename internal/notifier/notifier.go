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

// templateData keeps the event payload available at its existing top-level
// paths (for example ${rss.link}) and adds notification-specific values under
// the message namespace. A shallow copy prevents notifier rendering from
// mutating the scheduler-owned data map.
func templateData(message Message) map[string]any {
	data := make(map[string]any, len(message.Data)+1)
	for key, value := range message.Data {
		data[key] = value
	}
	data["message"] = map[string]any{
		"subject": message.Subject,
		"body":    message.Body,
	}
	return data
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
