package webhook

import (
	"net/http"
	"testing"
	"time"
)

func TestNewClient_Defaults(t *testing.T) {
	cl := NewClient()
	if cl.retries != defaultRetries {
		t.Fatalf("expected retries %d, got %d", defaultRetries, cl.retries)
	}
	if cl.timeout != defaultTimeout {
		t.Fatalf("expected timeout %v, got %v", defaultTimeout, cl.timeout)
	}
	if cl.httpClient == nil {
		t.Fatal("httpClient should not be nil")
	}
}

func TestNewClient_WithOptions(t *testing.T) {
	cl := NewClient(
		WithTimeout(30*time.Second),
		WithRetries(5),
	)
	if cl.retries != 5 {
		t.Fatalf("expected retries 5, got %d", cl.retries)
	}
	if cl.timeout != 30*time.Second {
		t.Fatalf("expected timeout 30s, got %v", cl.timeout)
	}
	if cl.httpClient.Timeout != 30*time.Second {
		t.Fatalf("httpClient timeout should match, got %v", cl.httpClient.Timeout)
	}
}

func TestNewClient_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	cl := NewClient(WithHTTPClient(custom))
	if cl.httpClient != custom {
		t.Fatal("expected custom http client to be used")
	}
}

func TestWithRetries_NegativeClampedToZero(t *testing.T) {
	cl := NewClient(WithRetries(-1))
	if cl.retries != 0 {
		t.Fatalf("expected retries 0, got %d", cl.retries)
	}
}
