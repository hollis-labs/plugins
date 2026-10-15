package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gm "google.golang.org/api/gmail/v1"
)

// defaultScopes returns the scopes used when Config.Scopes is empty.
func defaultScopes() []string {
	return []string{
		gm.GmailSendScope,
		gm.GmailReadonlyScope,
	}
}

// loadOAuthConfig reads the credentials.json file and returns an oauth2.Config.
func loadOAuthConfig(credentialsFile string, scopes []string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("gmail: unable to read credentials file %q: %w", credentialsFile, err)
	}

	config, err := google.ConfigFromJSON(b, scopes...)
	if err != nil {
		return nil, fmt.Errorf("gmail: unable to parse credentials: %w", err)
	}

	return config, nil
}

// tokenFromFile reads an OAuth2 token from a JSON file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// saveToken writes an OAuth2 token to a JSON file.
func saveToken(path string, token *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("gmail: unable to save token to %q: %w", path, err)
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(token)
}

// getTokenInteractive runs the OAuth2 authorization code flow via stdin.
// It prints a URL for the user to visit, then reads the authorization code.
func getTokenInteractive(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the authorization code:\n%v\n\nEnter code: ", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("gmail: unable to read authorization code: %w", err)
	}

	tok, err := config.Exchange(ctx, authCode)
	if err != nil {
		return nil, fmt.Errorf("gmail: unable to exchange authorization code: %w", err)
	}

	return tok, nil
}

// getToken retrieves a valid OAuth2 token. It first checks for an existing
// token file; if not found, it runs the interactive flow and saves the result.
// The returned token source will auto-refresh expired tokens.
func getToken(ctx context.Context, config *oauth2.Config, tokenFile string) (oauth2.TokenSource, error) {
	tok, err := tokenFromFile(tokenFile)
	if err != nil {
		// No saved token — run interactive flow
		tok, err = getTokenInteractive(ctx, config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenFile, tok); err != nil {
			return nil, err
		}
	}

	// ReuseTokenSource auto-refreshes expired tokens
	return config.TokenSource(ctx, tok), nil
}
