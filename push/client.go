package push

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// oauth2Client signs every request with a fresh access token.
func oauth2Client(ctx context.Context, creds *google.Credentials) *http.Client {
	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = 15 * time.Second
	return client
}
