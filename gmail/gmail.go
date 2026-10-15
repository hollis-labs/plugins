package gmail

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	gm "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Client wraps the Gmail API service.
type Client struct {
	service *gm.Service
}

// NewClient creates a new Gmail client using the provided configuration.
// It handles OAuth2 authentication, including the interactive first-time flow.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = defaultScopes()
	}

	oauthCfg, err := loadOAuthConfig(cfg.CredentialsFile, scopes)
	if err != nil {
		return nil, err
	}

	tokenSource, err := getToken(ctx, oauthCfg, cfg.TokenFile)
	if err != nil {
		return nil, err
	}

	svc, err := gm.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("gmail: unable to create service: %w", err)
	}

	return &Client{service: svc}, nil
}

// NewClientWithTokenSource creates a Client from an existing oauth2.TokenSource.
// This is useful for testing or when the caller manages authentication externally.
func NewClientWithTokenSource(ctx context.Context, ts oauth2.TokenSource) (*Client, error) {
	svc, err := gm.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("gmail: unable to create service: %w", err)
	}

	return &Client{service: svc}, nil
}
