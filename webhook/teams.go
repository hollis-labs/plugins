package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// AdaptiveCard builds a Microsoft Teams Adaptive Card using a fluent API.
type AdaptiveCard struct {
	title   string
	texts   []string
	facts   []map[string]string
	actions []map[string]string
}

// NewCard creates a new empty AdaptiveCard builder.
func NewCard() *AdaptiveCard {
	return &AdaptiveCard{}
}

// Title sets the card's title (rendered as a large, bold TextBlock).
func (c *AdaptiveCard) Title(t string) *AdaptiveCard {
	c.title = t
	return c
}

// Text adds a text paragraph to the card body.
func (c *AdaptiveCard) Text(t string) *AdaptiveCard {
	c.texts = append(c.texts, t)
	return c
}

// Facts adds a set of key-value facts displayed as a FactSet.
func (c *AdaptiveCard) Facts(facts map[string]string) *AdaptiveCard {
	c.facts = append(c.facts, facts)
	return c
}

// Action adds an Action.OpenUrl button to the card.
func (c *AdaptiveCard) Action(title, url string) *AdaptiveCard {
	c.actions = append(c.actions, map[string]string{"title": title, "url": url})
	return c
}

// Build returns the Adaptive Card as a map ready for JSON serialization.
func (c *AdaptiveCard) Build() map[string]interface{} {
	body := make([]interface{}, 0)

	if c.title != "" {
		body = append(body, map[string]interface{}{
			"type":   "TextBlock",
			"text":   c.title,
			"size":   "Large",
			"weight": "Bolder",
		})
	}

	for _, t := range c.texts {
		body = append(body, map[string]interface{}{
			"type": "TextBlock",
			"text": t,
			"wrap": true,
		})
	}

	for _, factMap := range c.facts {
		facts := make([]map[string]string, 0, len(factMap))
		for k, v := range factMap {
			facts = append(facts, map[string]string{"title": k, "value": v})
		}
		body = append(body, map[string]interface{}{
			"type":  "FactSet",
			"facts": facts,
		})
	}

	actions := make([]interface{}, 0, len(c.actions))
	for _, a := range c.actions {
		actions = append(actions, map[string]interface{}{
			"type":  "Action.OpenUrl",
			"title": a["title"],
			"url":   a["url"],
		})
	}

	card := map[string]interface{}{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}

	if len(actions) > 0 {
		card["actions"] = actions
	}

	return card
}

// buildTeamsPayload wraps an AdaptiveCard in the Teams Incoming Webhook envelope.
func buildTeamsPayload(card *AdaptiveCard) map[string]interface{} {
	return map[string]interface{}{
		"type": "message",
		"attachments": []interface{}{
			map[string]interface{}{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content":     card.Build(),
			},
		},
	}
}

// SendTeams sends an AdaptiveCard to a Microsoft Teams Incoming Webhook URL.
func (cl *Client) SendTeams(webhookURL string, card *AdaptiveCard) error {
	payload := buildTeamsPayload(card)

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook: marshal teams payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("webhook: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cl.doWithRetry(req)
	if err != nil {
		return fmt.Errorf("webhook: send teams card: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &WebhookError{StatusCode: resp.StatusCode, Body: string(bodyBytes)}
	}

	return nil
}
