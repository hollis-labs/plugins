# Gmail Connector

Reusable Gmail API client library for Fragments Engine. Wraps Google's official Go SDK (`google.golang.org/api/gmail/v1`) with a simple interface for sending and reading email via OAuth2.

## Setup

### 1. Create a Google Cloud Project

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project (or select an existing one)
3. Navigate to **APIs & Services > Library**
4. Search for **Gmail API** and click **Enable**

### 2. Create OAuth2 Credentials

1. Go to **APIs & Services > Credentials**
2. Click **Create Credentials > OAuth client ID**
3. If prompted, configure the OAuth consent screen first (External is fine for testing)
4. Select application type: **Desktop app**
5. Name it (e.g., "Fragments Engine Gmail")
6. Click **Create**
7. Download the JSON file and save it as `credentials.json`

### 3. First Run — Authorization

On first use, the library will:

1. Print a URL to your terminal
2. Open your browser (or you paste the URL manually)
3. Sign in with your Google account and grant the requested permissions
4. Copy the authorization code back to the terminal
5. The library exchanges the code for tokens and saves them to `token.json`

Subsequent runs reuse the saved token. The library auto-refreshes expired access tokens using the stored refresh token.

## Usage

```go
package main

import (
    "context"
    "fmt"
    "log"

    gmail "github.com/hollis-labs/fragments-engine/connectors/gmail"
)

func main() {
    ctx := context.Background()

    client, err := gmail.NewClient(ctx, gmail.Config{
        CredentialsFile: "credentials.json",
        TokenFile:       "token.json",
    })
    if err != nil {
        log.Fatal(err)
    }

    // Send an email
    err = client.SendEmail(ctx, gmail.SendOpts{
        To:      []string{"recipient@example.com"},
        Subject: "Hello from Fragments Engine",
        Body:    "This is a test email.",
    })
    if err != nil {
        log.Fatal(err)
    }

    // List recent unread messages
    emails, err := client.ListMessages(ctx, "is:unread", 5)
    if err != nil {
        log.Fatal(err)
    }

    for _, e := range emails {
        fmt.Printf("[%s] %s — %s\n", e.Date.Format("2006-01-02"), e.From, e.Subject)
    }

    // Get a specific message
    if len(emails) > 0 {
        full, err := client.GetMessage(ctx, emails[0].ID)
        if err != nil {
            log.Fatal(err)
        }
        fmt.Println(full.Body)
    }
}
```

## API

### `NewClient(ctx, Config) (*Client, error)`

Creates a new Gmail client. Handles OAuth2 flow automatically.

### `Client.SendEmail(ctx, SendOpts) error`

Sends an email. Formats as RFC 2822, base64url encodes, and posts via Gmail API.

### `Client.ListMessages(ctx, query, maxResults) ([]Email, error)`

Queries the mailbox using Gmail search syntax. Returns parsed Email structs.

### `Client.GetMessage(ctx, id) (*Email, error)`

Fetches a single message by ID with full body content.

## Files

| File | Purpose |
|------|---------|
| `types.go` | Config, SendOpts, Email type definitions |
| `gmail.go` | Client struct and constructor |
| `auth.go` | OAuth2 flow: credentials, token storage, refresh |
| `send.go` | Email sending with RFC 2822 formatting |
| `read.go` | Inbox reading with MIME body extraction |

## Testing

```bash
go test ./...
```

Tests cover email formatting and message parsing without network calls.

## Dependencies

- `google.golang.org/api/gmail/v1` — Google Gmail API client
- `golang.org/x/oauth2/google` — OAuth2 for Google APIs

No Conduit, plugin, or Fragments Engine internal imports.
