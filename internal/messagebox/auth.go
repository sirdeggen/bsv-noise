package messagebox

import (
	"context"
	"io"
	"net/http"

	clients "github.com/bsv-blockchain/go-sdk/auth/clients/authhttp"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

// AuthPoster performs BRC-31 authenticated POSTs with the wallet identity.
type AuthPoster struct {
	Fetch *clients.AuthFetch
}

// NewAuthPoster builds an AuthFetch client. wallet must be the same identity
// that owns the Message Box. Logging is left to the caller; pass a fetch
// built with clients.WithoutLogging when the process must not emit request
// bodies.
func NewAuthPoster(wallet sdk.Interface) AuthPoster {
	return AuthPoster{Fetch: clients.New(wallet, clients.WithoutLogging())}
}

func (a AuthPoster) Post(ctx context.Context, url string, body []byte) (int, []byte, error) {
	resp, err := a.Fetch.Fetch(ctx, url, &clients.SimplifiedFetchRequestOptions{
		Method:  http.MethodPost,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	})
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, buf, nil
}
