package notifier

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

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
	return sendMail(ctx, addr, cfg, message)
}

func sendMail(ctx context.Context, addr string, cfg EmailConfig, message Message) error {
	from, err := mail.ParseAddress(strings.TrimSpace(cfg.From))
	if err != nil {
		return fmt.Errorf("parse email sender: %w", err)
	}
	recipients := make([]string, 0, len(cfg.To))
	for _, raw := range cfg.To {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		recipient, err := mail.ParseAddress(raw)
		if err != nil {
			return fmt.Errorf("parse email recipient: %w", err)
		}
		recipients = append(recipients, recipient.Address)
	}
	if len(recipients) == 0 {
		return fmt.Errorf("email recipient is required")
	}

	tlsConfig := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var connection net.Conn
	err = nil
	if cfg.ImplicitTLS {
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: tlsConfig}
		connection, err = tlsDialer.DialContext(ctx, "tcp", addr)
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline := time.Now().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stopCancellation()

	client, err := smtp.NewClient(connection, cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()

	if cfg.StartTLS && !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP server does not support required STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	for _, to := range recipients {
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
