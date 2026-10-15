package webhook

import (
	"errors"
	"math"
	"net/http"
	"time"
)

const (
	baseDelay = 500 * time.Millisecond
	maxDelay  = 10 * time.Second
)

// doWithRetry executes the request with exponential backoff. It retries on
// network errors and 5xx status codes up to cl.retries times.
func (cl *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= cl.retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
			if delay > maxDelay {
				delay = maxDelay
			}
			time.Sleep(delay)
		}

		resp, err := cl.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Don't retry client errors (4xx) — only server errors (5xx).
		if resp.StatusCode >= 500 {
			lastErr = &WebhookError{StatusCode: resp.StatusCode}
			// Drain and close body so the connection can be reused.
			resp.Body.Close()
			continue
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("webhook: request failed after retries")
	}
	return nil, lastErr
}
