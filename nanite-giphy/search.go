package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Result is the shape returned by Search and serialized into both the
// command envelope and the MCP tool Content payload.
type Result struct {
	Title  string `json:"title"`
	GifURL string `json:"gif_url"`
	Source string `json:"source"`
	Query  string `json:"query"`
}

// giphyAPIResponse mirrors the relevant subset of Giphy's
// /v1/gifs/search JSON response.
type giphyAPIResponse struct {
	Data []struct {
		Title  string `json:"title"`
		Images struct {
			Original struct {
				URL string `json:"url"`
			} `json:"original"`
			FixedWidth struct {
				URL string `json:"url"`
			} `json:"fixed_width"`
		} `json:"images"`
	} `json:"data"`
}

// demoGifs is a small fallback set used when no API key is configured
// so the plugin can be exercised end-to-end during development without
// a Giphy account.
var demoGifs = map[string]string{
	"celebration": "https://media.giphy.com/media/g9582DNuQppxC/giphy.gif",
	"success":     "https://media.giphy.com/media/a0h7sAqON67nO/giphy.gif",
	"cat":         "https://media.giphy.com/media/JIX9t2j0ZTN9S/giphy.gif",
	"cats":        "https://media.giphy.com/media/JIX9t2j0ZTN9S/giphy.gif",
	"dog":         "https://media.giphy.com/media/4Zo41lhzKt6iZ8xff9/giphy.gif",
	"coding":      "https://media.giphy.com/media/ZVik7pBtu9dNS/giphy.gif",
	"coffee":      "https://media.giphy.com/media/DrJm6F9poo4aA/giphy.gif",
	"rocket":      "https://media.giphy.com/media/mi6DsSSNKDbUY/giphy.gif",
	"fire":        "https://media.giphy.com/media/j3IxJRLNLZz9sXR7ZA/giphy.gif",
	"balloons":    "https://media.giphy.com/media/26DNdtFJpLXJiwe2Q/giphy.gif",
}

// Search returns a single Giphy result for query. When apiKey is empty
// it returns a deterministic pick from demoGifs. A (nil, nil) return
// means the API responded successfully but with no matches.
func Search(ctx context.Context, client *http.Client, apiKey, rating, query string) (*Result, error) {
	query = strings.TrimSpace(query)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(query) > 1024 {
		return nil, fmt.Errorf("query exceeds 1024 bytes")
	}
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if apiKey == "" {
		return searchDemo(query), nil
	}
	if rating == "" {
		rating = "g"
	}
	return searchLive(ctx, client, apiKey, rating, query)
}

func searchDemo(query string) *Result {
	lower := strings.ToLower(query)
	var gifURL string
	keys := make([]string, 0, len(demoGifs))
	for key := range demoGifs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, keyword := range keys {
		if strings.Contains(lower, keyword) {
			gifURL = demoGifs[keyword]
			break
		}
	}
	if gifURL == "" {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(query))
		gifURL = demoGifs[keys[int(hash.Sum32())%len(keys)]]
	}
	return &Result{
		Title:  fmt.Sprintf("Here's your %s!", query),
		GifURL: gifURL,
		Source: "GIPHY (demo mode)",
		Query:  query,
	}
}

func searchLive(ctx context.Context, client *http.Client, apiKey, rating, query string) (*Result, error) {
	q := url.Values{}
	q.Set("api_key", apiKey)
	q.Set("q", query)
	q.Set("limit", "1")
	q.Set("rating", rating)
	reqURL := "https://api.giphy.com/v1/gifs/search?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("giphy request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("giphy returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > 256*1024 {
		return nil, fmt.Errorf("giphy response exceeds limit")
	}
	var api giphyAPIResponse
	if err := json.Unmarshal(body, &api); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(api.Data) == 0 {
		return nil, nil
	}

	gif := api.Data[0]
	gifURL := gif.Images.Original.URL
	if gifURL == "" {
		gifURL = gif.Images.FixedWidth.URL
	}
	parsed, err := url.Parse(gifURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || (parsed.Hostname() != "giphy.com" && !strings.HasSuffix(parsed.Hostname(), ".giphy.com")) {
		return nil, fmt.Errorf("giphy returned an invalid GIF URL")
	}
	return &Result{
		Title:  fmt.Sprintf("Here's your %s!", query),
		GifURL: gifURL,
		Source: "GIPHY",
		Query:  query,
	}, nil
}
