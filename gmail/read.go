package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	gm "google.golang.org/api/gmail/v1"
)

// ListMessages queries the user's mailbox and returns a slice of Email.
// The query parameter uses the same syntax as the Gmail search box
// (e.g., "is:unread", "from:alice@example.com", "subject:hello").
// maxResults limits how many messages to return (0 defaults to 10).
func (c *Client) ListMessages(ctx context.Context, query string, maxResults int) ([]Email, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	call := c.service.Users.Messages.List("me").Context(ctx).MaxResults(int64(maxResults))
	if query != "" {
		call = call.Q(query)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: failed to list messages: %w", err)
	}

	emails := make([]Email, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		email, err := c.GetMessage(ctx, m.Id)
		if err != nil {
			return nil, err
		}
		emails = append(emails, *email)
	}

	return emails, nil
}

// GetMessage fetches a single message by ID and returns a parsed Email.
func (c *Client) GetMessage(ctx context.Context, id string) (*Email, error) {
	msg, err := c.service.Users.Messages.Get("me", id).Context(ctx).Format("full").Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: failed to get message %s: %w", id, err)
	}

	return parseMessage(msg), nil
}

// parseMessage converts a Gmail API Message into our Email type.
func parseMessage(msg *gm.Message) *Email {
	email := &Email{
		ID:      msg.Id,
		Snippet: msg.Snippet,
		Labels:  msg.LabelIds,
	}

	// Extract headers
	if msg.Payload != nil {
		for _, h := range msg.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "from":
				email.From = h.Value
			case "to":
				email.To = h.Value
			case "subject":
				email.Subject = h.Value
			case "date":
				email.Date = parseDate(h.Value)
			}
		}

		email.Body = extractBody(msg.Payload)
	}

	// Fallback: use internal date if header parsing failed
	if email.Date.IsZero() && msg.InternalDate != 0 {
		email.Date = time.UnixMilli(msg.InternalDate)
	}

	return email
}

// parseDate attempts to parse an RFC 2822 date string.
func parseDate(s string) time.Time {
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"2 Jan 2006 15:04:05 -0700",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// extractBody walks the MIME structure and returns the best text body.
// It prefers text/plain but falls back to text/html.
func extractBody(payload *gm.MessagePart) string {
	if payload == nil {
		return ""
	}

	// Single-part message
	if payload.MimeType == "text/plain" && payload.Body != nil && payload.Body.Data != "" {
		return decodeBase64URL(payload.Body.Data)
	}

	// Multipart: look for text/plain first, then text/html
	var htmlBody string
	for _, part := range payload.Parts {
		switch part.MimeType {
		case "text/plain":
			if part.Body != nil && part.Body.Data != "" {
				return decodeBase64URL(part.Body.Data)
			}
		case "text/html":
			if part.Body != nil && part.Body.Data != "" {
				htmlBody = decodeBase64URL(part.Body.Data)
			}
		case "multipart/alternative", "multipart/mixed", "multipart/related":
			// Recurse into nested multipart
			if body := extractBody(part); body != "" {
				return body
			}
		}
	}

	if htmlBody != "" {
		return htmlBody
	}

	// Last resort: single-part HTML
	if payload.MimeType == "text/html" && payload.Body != nil && payload.Body.Data != "" {
		return decodeBase64URL(payload.Body.Data)
	}

	return ""
}

// decodeBase64URL decodes a base64url-encoded string (no padding).
func decodeBase64URL(s string) string {
	data, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		// Try without padding
		data, err = base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return s // return raw on failure
		}
	}
	return string(data)
}
