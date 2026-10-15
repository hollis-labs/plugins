package gmail

import (
	"encoding/base64"
	"testing"
	"time"

	gm "google.golang.org/api/gmail/v1"
)

func TestParseMessage_BasicHeaders(t *testing.T) {
	msg := &gm.Message{
		Id:      "msg-123",
		Snippet: "Hello there...",
		Payload: &gm.MessagePart{
			Headers: []*gm.MessagePartHeader{
				{Name: "From", Value: "alice@example.com"},
				{Name: "To", Value: "bob@example.com"},
				{Name: "Subject", Value: "Test Subject"},
				{Name: "Date", Value: "Mon, 20 Mar 2026 10:00:00 -0500"},
			},
			MimeType: "text/plain",
			Body: &gm.MessagePartBody{
				Data: base64.URLEncoding.EncodeToString([]byte("Hello body")),
			},
		},
		LabelIds: []string{"INBOX", "UNREAD"},
	}

	email := parseMessage(msg)

	assertEqual(t, "ID", email.ID, "msg-123")
	assertEqual(t, "From", email.From, "alice@example.com")
	assertEqual(t, "To", email.To, "bob@example.com")
	assertEqual(t, "Subject", email.Subject, "Test Subject")
	assertEqual(t, "Snippet", email.Snippet, "Hello there...")
	assertEqual(t, "Body", email.Body, "Hello body")

	if len(email.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(email.Labels))
	}

	expectedDate := time.Date(2026, 3, 20, 10, 0, 0, 0, time.FixedZone("", -5*3600))
	if !email.Date.Equal(expectedDate) {
		t.Errorf("expected date %v, got %v", expectedDate, email.Date)
	}
}

func TestParseMessage_MultipartPreferPlain(t *testing.T) {
	msg := &gm.Message{
		Id: "msg-456",
		Payload: &gm.MessagePart{
			MimeType: "multipart/alternative",
			Headers:  []*gm.MessagePartHeader{},
			Parts: []*gm.MessagePart{
				{
					MimeType: "text/plain",
					Body: &gm.MessagePartBody{
						Data: base64.URLEncoding.EncodeToString([]byte("plain text body")),
					},
				},
				{
					MimeType: "text/html",
					Body: &gm.MessagePartBody{
						Data: base64.URLEncoding.EncodeToString([]byte("<p>html body</p>")),
					},
				},
			},
		},
	}

	email := parseMessage(msg)
	assertEqual(t, "Body", email.Body, "plain text body")
}

func TestParseMessage_MultipartFallbackHTML(t *testing.T) {
	msg := &gm.Message{
		Id: "msg-789",
		Payload: &gm.MessagePart{
			MimeType: "multipart/alternative",
			Headers:  []*gm.MessagePartHeader{},
			Parts: []*gm.MessagePart{
				{
					MimeType: "text/html",
					Body: &gm.MessagePartBody{
						Data: base64.URLEncoding.EncodeToString([]byte("<p>only html</p>")),
					},
				},
			},
		},
	}

	email := parseMessage(msg)
	assertEqual(t, "Body", email.Body, "<p>only html</p>")
}

func TestParseMessage_InternalDateFallback(t *testing.T) {
	// No Date header — should fall back to InternalDate
	msg := &gm.Message{
		Id:           "msg-date",
		InternalDate: 1774274400000, // 2026-03-20T15:00:00Z in millis
		Payload: &gm.MessagePart{
			Headers: []*gm.MessagePartHeader{},
		},
	}

	email := parseMessage(msg)
	if email.Date.IsZero() {
		t.Error("expected non-zero date from InternalDate fallback")
	}
}

func TestDecodeBase64URL(t *testing.T) {
	original := "Hello, Gmail!"
	encoded := base64.URLEncoding.EncodeToString([]byte(original))

	result := decodeBase64URL(encoded)
	assertEqual(t, "decoded", result, original)
}

func TestDecodeBase64URL_RawNoPadding(t *testing.T) {
	original := "Test"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(original))

	result := decodeBase64URL(encoded)
	assertEqual(t, "decoded", result, original)
}

func TestParseDate_Formats(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"Mon, 20 Mar 2026 10:00:00 -0500", true},
		{"20 Mar 2026 10:00:00 -0500", true},
		{"2026-03-20T10:00:00Z", true},
		{"not-a-date", false},
	}

	for _, tt := range tests {
		result := parseDate(tt.input)
		if tt.valid && result.IsZero() {
			t.Errorf("expected valid date for %q, got zero", tt.input)
		}
		if !tt.valid && !result.IsZero() {
			t.Errorf("expected zero date for %q, got %v", tt.input, result)
		}
	}
}

func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}
