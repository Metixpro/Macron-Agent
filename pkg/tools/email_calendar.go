package tools

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
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

func SendEmail(ctx context.Context, host string, port int, user, pass, from, to, subject, body string) error {
	if host == "" {
		host = os.Getenv("MACRON_SMTP_HOST")
	}
	if port <= 0 {
		port = 587
	}
	if user == "" {
		user = os.Getenv("MACRON_SMTP_USER")
	}
	if pass == "" {
		pass = os.Getenv("MACRON_SMTP_PASS")
	}
	if from == "" {
		from = user
	}

	if host == "" {
		return fmt.Errorf("SMTP is not set up: macron config set MACRON_SMTP_HOST smtp.example.com (and MACRON_SMTP_USER, MACRON_SMTP_PASS)")
	}
	oneLine := strings.NewReplacer("\r", " ", "\n", " ")
	subject, to, from = oneLine.Replace(subject), oneLine.Replace(to), oneLine.Replace(from)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	toAddresses := strings.Split(to, ",")
	for i := range toAddresses {
		toAddresses[i] = strings.TrimSpace(toAddresses[i])
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		from, strings.Join(toAddresses, ", "), subject, body)

	auth := smtp.PlainAuth("", user, pass, host)

	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("connect to smtp server %s: %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client error: %w", err)
	}
	defer client.Quit()

	if ok, _ := client.Extension("STARTTLS"); ok {
		config := &tls.Config{ServerName: host}
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("starttls error: %w", err)
		}
	}

	if auth != nil && user != "" {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM failed: %w", err)
	}
	for _, rec := range toAddresses {
		if err := client.Rcpt(rec); err != nil {
			return fmt.Errorf("smtp RCPT TO %s failed: %w", rec, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA failed: %w", err)
	}
	_, err = w.Write([]byte(msg))
	if err != nil {
		return err
	}
	return w.Close()
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
