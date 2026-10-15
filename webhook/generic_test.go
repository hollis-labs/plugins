package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json content-type, got %s", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok123" {
			t.Errorf("expected Authorization header, got %s", auth)
		}

		body, _ := io.ReadAll(r.Body)
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err != nil {
			t.Errorf("invalid JSON body: %v", err)
		}
		if data["event"] != "deploy" {
			t.Errorf("expected event deploy, got %v", data["event"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cl := NewClient(WithRetries(0))
	resp, err := cl.SendJSON(srv.URL, map[string]string{"event": "deploy"}, map[string]string{
		"Authorization": "Bearer tok123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSendJSON_NilHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cl := NewClient(WithRetries(0))
	resp, err := cl.SendJSON(srv.URL, "hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestSendJSON_ServerError_NoRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cl := NewClient(WithRetries(0))
	_, err := cl.SendJSON(srv.URL, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call with 0 retries, got %d", calls)
	}
}

func TestSendJSON_Retry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cl := NewClient(WithRetries(3))
	resp, err := cl.SendJSON(srv.URL, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls != 3 {
		t.Fatalf("expected 3 calls (2 failures + 1 success), got %d", calls)
	}
}
