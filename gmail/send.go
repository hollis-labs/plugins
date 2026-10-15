package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	gm "google.golang.org/api/gmail/v1"
)

// SendEmail sends an email via the Gmail API.
func (c *Client) SendEmail(ctx context.Context, opts SendOpts) error {
	raw, err := buildRawMessage(opts)
	if err != nil {
		return err
	}

	msg := &gm.Message{
		Raw: raw,
	}

	_, err = c.service.Users.Messages.Send("me", msg).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("gmail: failed to send email: %w", err)
	}

	return nil
}

// buildRawMessage formats a SendOpts into an RFC 2822 message, then
// base64url-encodes it for the Gmail API.
func buildRawMessage(opts SendOpts) (string, error) {
	if len(opts.To) == 0 {
		return "", fmt.Errorf("gmail: at least one recipient is required")
	}

	msg := FormatRFC2822(opts)
	return base64.URLEncoding.EncodeToString([]byte(msg)), nil
}

// FormatRFC2822 builds an RFC 2822 message string from SendOpts.
// Exported for testing.
func FormatRFC2822(opts SendOpts) string {
	var b strings.Builder

	b.WriteString("To: ")
	b.WriteString(strings.Join(opts.To, ", "))
	b.WriteString("\r\n")

	if len(opts.CC) > 0 {
		b.WriteString("Cc: ")
		b.WriteString(strings.Join(opts.CC, ", "))
		b.WriteString("\r\n")
	}

	b.WriteString("Subject: ")
	b.WriteString(opts.Subject)
	b.WriteString("\r\n")

	b.WriteString("MIME-Version: 1.0\r\n")

	if opts.IsHTML {
		b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	} else {
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	}

	b.WriteString("\r\n")
	b.WriteString(opts.Body)

	return b.String()
}
