package notifier

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestBuildEmailSanitizesHeaderNewlines(t *testing.T) {
	email := buildEmail(EmailConfig{
		From: "sender@example.com\r\nBcc: attacker@example.com",
		To:   []string{"recipient@example.com"},
	}, Message{Subject: "Status\r\nBcc: attacker@example.com", Body: "All good"})

	if strings.Contains(email, "\r\nBcc: attacker@example.com") {
		t.Fatalf("email contains injected header: %q", email)
	}
	if !strings.Contains(email, "Status  Bcc: attacker@example.com") {
		t.Fatalf("sanitized subject not found: %q", email)
	}
}

func TestEmailNotifierRequiresAdvertisedStartTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprint(connection, "220 localhost ESMTP ready\r\n")
		_, _ = bufio.NewReader(connection).ReadString('\n')
		_, _ = fmt.Fprint(connection, "250-localhost\r\n250 HELP\r\n")
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(EmailConfig{Host: host, Port: port, From: "sender@example.com", To: []string{"recipient@example.com"}, StartTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = NewEmailNotifier().Send(ctx, model.NotifyChannel{Config: config}, Message{Subject: "subject", Body: "body"})
	if err == nil || !strings.Contains(err.Error(), "does not support required STARTTLS") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestEmailNotifierUsesMailboxAddressForSMTPEnvelope(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []string, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		_, _ = fmt.Fprint(connection, "220 localhost ESMTP ready\r\n")
		captured := make([]string, 0, 2)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
				_, _ = fmt.Fprint(connection, "250-localhost\r\n250 HELP\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				captured = append(captured, line)
				_, _ = fmt.Fprint(connection, "250 sender accepted\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				captured = append(captured, line)
				_, _ = fmt.Fprint(connection, "250 recipient accepted\r\n")
			case line == "DATA":
				_, _ = fmt.Fprint(connection, "354 end with dot\r\n")
				for {
					bodyLine, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(bodyLine) == "." {
						break
					}
				}
				_, _ = fmt.Fprint(connection, "250 queued\r\n")
				commands <- captured
				return
			default:
				_, _ = fmt.Fprint(connection, "500 unexpected command\r\n")
			}
		}
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := EmailConfig{
		Host: host, Port: port,
		From: "WatchBell <sender@example.com>",
		To:   []string{"Ops <recipient@example.com>"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sendMail(ctx, listener.Addr().String(), cfg, Message{Subject: "subject", Body: "body"}); err != nil {
		t.Fatal(err)
	}
	select {
	case captured := <-commands:
		if len(captured) != 2 || !strings.Contains(captured[0], "<sender@example.com>") || strings.Contains(captured[0], "WatchBell") || !strings.Contains(captured[1], "<recipient@example.com>") || strings.Contains(captured[1], "Ops") {
			t.Fatalf("SMTP envelope commands = %#v", captured)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
