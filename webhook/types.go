package webhook

import "fmt"

// WebhookError wraps an HTTP status code and response body for non-2xx responses.
type WebhookError struct {
	StatusCode int
	Body       string
}

func (e *WebhookError) Error() string {
	return fmt.Sprintf("webhook: HTTP %d: %s", e.StatusCode, e.Body)
}

// RetryableError indicates whether the underlying error is worth retrying.
type RetryableError struct {
	Err       error
	Retryable bool
}

func (e *RetryableError) Error() string {
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}
