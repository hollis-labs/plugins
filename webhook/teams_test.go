package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewCard_Empty(t *testing.T) {
	card := NewCard().Build()

	if card["type"] != "AdaptiveCard" {
		t.Fatalf("expected type AdaptiveCard, got %v", card["type"])
	}
	if card["version"] != "1.4" {
		t.Fatalf("expected version 1.4, got %v", card["version"])
	}
	body, ok := card["body"].([]interface{})
	if !ok {
		t.Fatal("body should be a slice")
	}
	if len(body) != 0 {
		t.Fatalf("expected empty body, got %d elements", len(body))
	}
	if _, hasActions := card["actions"]; hasActions {
		t.Fatal("empty card should not have actions key")
	}
}

func TestCard_Title(t *testing.T) {
	card := NewCard().Title("Hello").Build()

	body := card["body"].([]interface{})
	if len(body) != 1 {
		t.Fatalf("expected 1 body element, got %d", len(body))
	}
	block := body[0].(map[string]interface{})
	if block["text"] != "Hello" {
		t.Fatalf("expected title Hello, got %v", block["text"])
	}
	if block["size"] != "Large" {
		t.Fatalf("expected size Large, got %v", block["size"])
	}
	if block["weight"] != "Bolder" {
		t.Fatalf("expected weight Bolder, got %v", block["weight"])
	}
}

func TestCard_Text(t *testing.T) {
	card := NewCard().Text("Line 1").Text("Line 2").Build()

	body := card["body"].([]interface{})
	if len(body) != 2 {
		t.Fatalf("expected 2 body elements, got %d", len(body))
	}
	for i, text := range []string{"Line 1", "Line 2"} {
		block := body[i].(map[string]interface{})
		if block["text"] != text {
			t.Fatalf("body[%d]: expected text %q, got %v", i, text, block["text"])
		}
		if block["wrap"] != true {
			t.Fatalf("body[%d]: expected wrap true", i)
		}
	}
}

func TestCard_Facts(t *testing.T) {
	card := NewCard().Facts(map[string]string{"Status": "OK", "Version": "1.0"}).Build()

	body := card["body"].([]interface{})
	if len(body) != 1 {
		t.Fatalf("expected 1 body element, got %d", len(body))
	}
	factSet := body[0].(map[string]interface{})
	if factSet["type"] != "FactSet" {
		t.Fatalf("expected FactSet type, got %v", factSet["type"])
	}
	facts := factSet["facts"].([]map[string]string)
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	// Verify all expected facts are present (order may vary due to map iteration).
	found := map[string]string{}
	for _, f := range facts {
		found[f["title"]] = f["value"]
	}
	if found["Status"] != "OK" {
		t.Fatal("missing or wrong Status fact")
	}
	if found["Version"] != "1.0" {
		t.Fatal("missing or wrong Version fact")
	}
}

func TestCard_Actions(t *testing.T) {
	card := NewCard().Action("Open", "https://example.com").Build()

	actions, ok := card["actions"].([]interface{})
	if !ok {
		t.Fatal("actions should be a slice")
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	action := actions[0].(map[string]interface{})
	if action["type"] != "Action.OpenUrl" {
		t.Fatalf("expected Action.OpenUrl, got %v", action["type"])
	}
	if action["title"] != "Open" {
		t.Fatalf("expected title Open, got %v", action["title"])
	}
	if action["url"] != "https://example.com" {
		t.Fatalf("expected url https://example.com, got %v", action["url"])
	}
}

func TestCard_FullBuild(t *testing.T) {
	card := NewCard().
		Title("Deploy Alert").
		Text("Service deployed successfully.").
		Facts(map[string]string{"Env": "prod", "Region": "us-east-1"}).
		Action("View Dashboard", "https://dash.example.com")

	built := card.Build()

	// Verify it serializes to valid JSON.
	data, err := json.Marshal(built)
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}

	if parsed["$schema"] != "http://adaptivecards.io/schemas/adaptive-card.json" {
		t.Fatal("schema mismatch")
	}
}

func TestBuildTeamsPayload(t *testing.T) {
	card := NewCard().Title("Test")
	payload := buildTeamsPayload(card)

	if payload["type"] != "message" {
		t.Fatalf("expected type message, got %v", payload["type"])
	}

	attachments, ok := payload["attachments"].([]interface{})
	if !ok || len(attachments) != 1 {
		t.Fatal("expected 1 attachment")
	}

	att := attachments[0].(map[string]interface{})
	if att["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Fatalf("wrong content type: %v", att["contentType"])
	}

	content, ok := att["content"].(map[string]interface{})
	if !ok {
		t.Fatal("content should be a map")
	}
	if content["type"] != "AdaptiveCard" {
		t.Fatalf("expected AdaptiveCard, got %v", content["type"])
	}
}

func TestSendTeams_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}

		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("invalid JSON: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cl := NewClient(WithRetries(0))
	err := cl.SendTeams(srv.URL, NewCard().Title("Test"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendTeams_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	cl := NewClient(WithRetries(0))
	err := cl.SendTeams(srv.URL, NewCard().Title("Test"))
	if err == nil {
		t.Fatal("expected error for 400 status")
	}

	we, ok := err.(*WebhookError)
	if !ok {
		t.Fatalf("expected WebhookError, got %T: %v", err, err)
	}
	if we.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", we.StatusCode)
	}
}
