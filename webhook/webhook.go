// Package webhook provides a generic outbound webhook client with support for
// Microsoft Teams Incoming Webhooks (Adaptive Cards) and raw JSON POST requests.
//
// The Client is safe for concurrent use.
package webhook

import (
	"net/http"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	defaultRetries = 3
)

// Client sends outbound webhook requests. It is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	retries    int
	timeout    time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout sets the HTTP request timeout. Default is 10 seconds.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.timeout = d
	}
}

// WithRetries sets the maximum number of retry attempts for transient failures.
// Default is 3. Set to 0 to disable retries.
func WithRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.retries = n
	}
}

// WithHTTPClient replaces the default http.Client. The client's Timeout field
// will be overwritten by the Client's timeout setting.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// NewClient creates a new webhook Client with the given options.
func NewClient(opts ...Option) *Client {
	c := &Client{
		retries: defaultRetries,
		timeout: defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	c.httpClient.Timeout = c.timeout
	return c
}
