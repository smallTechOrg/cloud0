package labeler

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func newAuthedClient(ctx context.Context, creds *google.Credentials) *http.Client {
	hc := oauth2.NewClient(ctx, creds.TokenSource)
	hc.Timeout = 60 * time.Second
	return hc
}
