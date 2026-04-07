// Package gmail provides a reusable Gmail API client for Fragments Engine.
// It wraps Google's official Go SDK with a simple interface for sending
// and reading email via OAuth2.
package gmail

import "time"

// Config holds the settings needed to initialize a Gmail client.
type Config struct {
	// CredentialsFile is the path to credentials.json downloaded from Google Cloud Console.
	CredentialsFile string

	// TokenFile is the path where the OAuth2 token is stored after the first authorization.
	// If the file does not exist, the interactive authorization flow will run.
	TokenFile string

	// Scopes defines the Gmail API scopes to request.
	// If empty, defaults to GmailSendScope and GmailReadonlyScope.
	Scopes []string
}

// SendOpts defines the parameters for sending an email.
type SendOpts struct {
	To      []string // recipient addresses
	CC      []string // CC addresses (optional)
	Subject string
	Body    string // plain text or HTML content
	IsHTML  bool   // if true, Body is sent as text/html; otherwise text/plain
}

// Email represents a parsed Gmail message.
type Email struct {
	ID      string
	From    string
	To      string
	Subject string
	Date    time.Time
	Snippet string
	Body    string
	Labels  []string
}
