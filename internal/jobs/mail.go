package jobs

import (
	"context"
	"encoding/json"
	"net/smtp"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MailJobType sends a plain-text mail through SMTP — the only mail channel
// the port currently supports, matching upstream's VerifyEmail /
// ResetPassword notifications.
const MailJobType = "mail.send"

type mailPayload struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	HTML    string `json:"html,omitempty"`
}

// EnqueueMail queues an SMTP send so web requests never block on the mail
// server — the same decoupling upstream gets from its queue driver.
func EnqueueMail(ctx context.Context, pool *pgxpool.Pool, to, subject, body string) error {
	return Enqueue(ctx, pool, "default", MailJobType, mailPayload{To: to, Subject: subject, Body: body})
}

// EnqueueMailHTML queues an HTML-bodied send — upstream notification mails
// (invitations etc.) are HTML with an action link.
func EnqueueMailHTML(ctx context.Context, pool *pgxpool.Pool, to, subject, body, html string) error {
	return Enqueue(ctx, pool, "default", MailJobType, mailPayload{To: to, Subject: subject, Body: body, HTML: html})
}

// MailHandler delivers queued mail payloads. An empty smtpAddr disables
// delivery (mails vanish quietly, matching the old sendMail behavior).
func MailHandler(smtpAddr, mailFrom string) Handler {
	return func(ctx context.Context, data json.RawMessage) error {
		var p mailPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		if smtpAddr == "" {
			return nil
		}
		contentType := "text/plain"
		body := p.Body
		if p.HTML != "" {
			contentType = "text/html"
			body = p.HTML
		}
		msg := "From: " + mailFrom + "\r\nTo: " + p.To + "\r\nSubject: " + p.Subject +
			"\r\nContent-Type: " + contentType + "; charset=utf-8\r\n\r\n" + body + "\r\n"
		return smtp.SendMail(smtpAddr, nil, mailFrom, []string{p.To}, []byte(msg))
	}
}
