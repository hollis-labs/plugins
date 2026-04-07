package gmail

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestFormatRFC2822_PlainText(t *testing.T) {
	opts := SendOpts{
		To:      []string{"alice@example.com"},
		Subject: "Test Subject",
		Body:    "Hello, world!",
		IsHTML:  false,
	}

	msg := FormatRFC2822(opts)

	assertContains(t, msg, "To: alice@example.com\r\n")
	assertContains(t, msg, "Subject: Test Subject\r\n")
	assertContains(t, msg, "Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	assertContains(t, msg, "MIME-Version: 1.0\r\n")
	assertContains(t, msg, "\r\n\r\nHello, world!")
	assertNotContains(t, msg, "Cc:")
}

func TestFormatRFC2822_HTML(t *testing.T) {
	opts := SendOpts{
		To:      []string{"bob@example.com"},
		Subject: "HTML Test",
		Body:    "<h1>Hello</h1>",
		IsHTML:  true,
	}

	msg := FormatRFC2822(opts)

	assertContains(t, msg, "Content-Type: text/html; charset=\"UTF-8\"\r\n")
	assertContains(t, msg, "<h1>Hello</h1>")
}

func TestFormatRFC2822_MultipleRecipients(t *testing.T) {
	opts := SendOpts{
		To:      []string{"a@example.com", "b@example.com"},
		CC:      []string{"c@example.com"},
		Subject: "Multi",
		Body:    "test",
	}

	msg := FormatRFC2822(opts)

	assertContains(t, msg, "To: a@example.com, b@example.com\r\n")
	assertContains(t, msg, "Cc: c@example.com\r\n")
}

func TestFormatRFC2822_EmptyCC(t *testing.T) {
	opts := SendOpts{
		To:      []string{"a@example.com"},
		Subject: "No CC",
		Body:    "body",
	}

	msg := FormatRFC2822(opts)
	assertNotContains(t, msg, "Cc:")
}

func TestBuildRawMessage_NoRecipient(t *testing.T) {
	opts := SendOpts{
		Subject: "No To",
		Body:    "body",
	}

	_, err := buildRawMessage(opts)
	if err == nil {
		t.Fatal("expected error for empty To, got nil")
	}
}

func TestBuildRawMessage_ValidBase64(t *testing.T) {
	opts := SendOpts{
		To:      []string{"test@example.com"},
		Subject: "Encoding Test",
		Body:    "Hello",
	}

	raw, err := buildRawMessage(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("failed to decode base64url: %v", err)
	}

	if !strings.Contains(string(decoded), "To: test@example.com") {
		t.Error("decoded message missing To header")
	}
	if !strings.Contains(string(decoded), "Hello") {
		t.Error("decoded message missing body")
	}
}

func assertContains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("expected string to contain %q, got:\n%s", sub, s)
	}
}

func assertNotContains(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("expected string NOT to contain %q, got:\n%s", sub, s)
	}
}
