package notifier

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/watchbell/watchbell/internal/model"
)

type EmailNotifier struct{}

type EmailConfig struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	StartTLS    bool     `json:"startTls"`
	ImplicitTLS bool     `json:"implicitTls"`
	SkipVerify  bool     `json:"skipVerify"`
}

func NewEmailNotifier() *EmailNotifier {
	return &EmailNotifier{}
}

func (n *EmailNotifier) Type() string {
	return model.ChannelTypeEmail
}

func (n *EmailNotifier) Send(ctx context.Context, channel model.NotifyChannel, message Message) error {
	var cfg EmailConfig
	if err := json.Unmarshal(channel.Config, &cfg); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("email host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if strings.TrimSpace(cfg.From) == "" {
		cfg.From = cfg.Username
	}
	if strings.TrimSpace(cfg.From) == "" || len(cfg.To) == 0 {
		return fmt.Errorf("email from and to are required")
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	done := make(chan error, 1)
	go func() {
		done <- sendMail(addr, cfg, message)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func sendMail(addr string, cfg EmailConfig, message Message) error {
	var client *smtp.Client
	var err error
	tlsConfig := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec
	if cfg.ImplicitTLS {
		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return err
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return err
		}
	} else {
		client, err = smtp.Dial(addr)
		if err != nil {
			return err
		}
	}
	defer client.Close()

	if cfg.StartTLS && !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return err
			}
		}
	}
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return err
	}
	for _, to := range cfg.To {
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write([]byte(buildEmail(cfg, message))); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func buildEmail(cfg EmailConfig, message Message) string {
	headers := map[string]string{
		"From":         cfg.From,
		"To":           strings.Join(cfg.To, ", "),
		"Subject":      message.Subject,
		"MIME-Version": "1.0",
		"Content-Type": "text/plain; charset=utf-8",
	}
	var builder strings.Builder
	for key, value := range headers {
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(sanitizeHeader(value))
		builder.WriteString("\r\n")
	}
	builder.WriteString("\r\n")
	builder.WriteString(message.Body)
	builder.WriteString("\r\n")
	return builder.String()
}

func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
