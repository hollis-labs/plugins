package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// SendJSON sends an arbitrary JSON body to the given URL via HTTP POST.
// The body is marshalled to JSON. Additional headers can be provided via the
// headers map (Content-Type is set to application/json automatically unless
// overridden in headers).
func (cl *Client) SendJSON(url string, body interface{}, headers map[string]string) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("webhook: marshal json body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("webhook: create request: %w", err)
	}

	// Default content type; can be overridden by caller.
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := cl.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("webhook: send json: %w", err)
	}

	return resp, nil
}
