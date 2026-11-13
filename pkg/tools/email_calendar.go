package tools

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ToolEmailClient    = "email_client"
	ToolCalendarClient = "calendar_client"
)

func emailMessage(from, to, subject, body string) ([]byte, string, []string, error) {
	if strings.ContainsAny(from+to+subject, "\r\n") {
		return nil, "", nil, fmt.Errorf("email headers must not contain line breaks")
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return nil, "", nil, fmt.Errorf("invalid sender address: %w", err)
	}
	recipients, err := mail.ParseAddressList(to)
	if err != nil || len(recipients) == 0 {
		return nil, "", nil, fmt.Errorf("valid recipient addresses are required")
	}
	var addresses, headers []string
	for _, recipient := range recipients {
		addresses = append(addresses, recipient.Address)
		headers = append(headers, recipient.String())
	}
	var encoded bytes.Buffer
	writer := quotedprintable.NewWriter(&encoded)
	_, _ = writer.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")))
	_ = writer.Close()
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s", sender.String(), strings.Join(headers, ", "), mime.QEncoding.Encode("UTF-8", subject), encoded.String())
	return []byte(msg), sender.Address, addresses, nil
}

func SendEmail(ctx context.Context, host string, port int, user, pass, from, to, subject, body string) error {
	if host == "" {
		host = os.Getenv("MACRON_SMTP_HOST")
	}
	if port <= 0 {
		port = 587
		if value := os.Getenv("MACRON_SMTP_PORT"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("MACRON_SMTP_PORT must be between 1 and 65535")
			}
			port = n
		}
	}
	if user == "" {
		user = os.Getenv("MACRON_SMTP_USER")
	}
	if pass == "" {
		pass = os.Getenv("MACRON_SMTP_PASS")
	}
	if from == "" {
		from = firstEnv("MACRON_SMTP_FROM")
		if from == "" {
			from = user
		}
	}
	if host == "" {
		return fmt.Errorf("SMTP is not set up: set MACRON_SMTP_HOST, MACRON_SMTP_USER and MACRON_SMTP_PASS, or configure Gmail OAuth")
	}
	msg, sender, recipients, err := emailMessage(from, to, subject, body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if port == 465 || os.Getenv("MACRON_SMTP_TLS") == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to SMTP: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	send := func() error {
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("SMTP greeting: %w", err)
		}
		defer client.Close()
		if _, secure := conn.(*tls.Conn); !secure {
			if ok, _ := client.Extension("STARTTLS"); ok {
				if err := client.StartTLS(tlsConfig); err != nil {
					return fmt.Errorf("STARTTLS: %w", err)
				}
			}
		}
		if user != "" {
			if err := client.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
				return fmt.Errorf("SMTP authentication: %w", err)
			}
		}
		if err := client.Mail(sender); err != nil {
			return fmt.Errorf("SMTP MAIL FROM: %w", err)
		}
		for _, recipient := range recipients {
			if err := client.Rcpt(recipient); err != nil {
				return fmt.Errorf("SMTP RCPT TO: %w", err)
			}
		}
		w, err := client.Data()
		if err != nil {
			return fmt.Errorf("SMTP DATA: %w", err)
		}
		if _, err = w.Write(msg); err != nil {
			return err
		}
		if err = w.Close(); err != nil {
			return err
		}
		_ = client.Quit()
		return nil
	}
	err = send()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return err
}

func GenerateICalendar(title, description, location string, start, end time.Time) string {
	var sb strings.Builder
	now := time.Now().UTC().Format("20060102T150405Z")
	startStr := start.UTC().Format("20060102T150405Z")
	endStr := end.UTC().Format("20060102T150405Z")

	sb.WriteString("BEGIN:VCALENDAR\r\n")
	sb.WriteString("VERSION:2.0\r\n")
	sb.WriteString("PRODID:-//Macron Agent//Calendar 1.5//EN\r\n")
	sb.WriteString("CALSCALE:GREGORIAN\r\n")
	sb.WriteString("METHOD:PUBLISH\r\n")
	sb.WriteString("BEGIN:VEVENT\r\n")
	sb.WriteString(fmt.Sprintf("UID:macron-%d@localhost\r\n", time.Now().UnixNano()))
	sb.WriteString(fmt.Sprintf("DTSTAMP:%s\r\n", now))
	sb.WriteString(fmt.Sprintf("DTSTART:%s\r\n", startStr))
	sb.WriteString(fmt.Sprintf("DTEND:%s\r\n", endStr))
	sb.WriteString(fmt.Sprintf("SUMMARY:%s\r\n", title))
	if description != "" {
		sb.WriteString(fmt.Sprintf("DESCRIPTION:%s\r\n", strings.ReplaceAll(description, "\n", "\\n")))
	}
	if location != "" {
		sb.WriteString(fmt.Sprintf("LOCATION:%s\r\n", location))
	}
	sb.WriteString("STATUS:CONFIRMED\r\n")
	sb.WriteString("END:VEVENT\r\n")
	sb.WriteString("END:VCALENDAR\r\n")
	return sb.String()
}
