// Package messagebox speaks the go-message-box-server HTTP API.
//
// listMessages and acknowledgeMessage require BRC-31 authentication. The
// production poster is AuthFetch from go-sdk, using the same wallet identity
// that internalizes the payment. Tests pass a plain HTTP poster against a
// fixture server that already checked the JSON shape.
package messagebox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/sirdeggen/bsv-noise/internal/peerpay"
)

// Poster sends one JSON POST and returns the status and body.
type Poster interface {
	Post(ctx context.Context, url string, body []byte) (int, []byte, error)
}

// HTTPPoster is the unauthenticated transport used by protocol fixtures.
type HTTPPoster struct {
	Client *http.Client
}

func (h HTTPPoster) Post(ctx context.Context, url string, body []byte) (int, []byte, error) {
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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

// Client lists and acknowledges one message box.
type Client struct {
	BaseURL    string
	Box        string
	Poster     Poster
	Wallet     sdk.Interface
	Originator string
	Log        *slog.Logger
}

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}

type listRequest struct {
	MessageBox string `json:"messageBox"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

type listed struct {
	MessageID string          `json:"messageId"`
	Body      json.RawMessage `json:"body"`
	Sender    string          `json:"sender"`
}

type listResponse struct {
	Status      string   `json:"status"`
	Code        string   `json:"code"`
	Description string   `json:"description"`
	Messages    []listed `json:"messages"`
	HasMore     bool     `json:"hasMore"`
	NextOffset  int      `json:"nextOffset"`
}

// ListPayments reads payment_inbox (or the configured box) and parses PeerPay
// tokens. Messages that are not payment tokens are returned as errors paired
// with their ids so the sweeper can record them without acknowledging them.
func (c *Client) ListPayments(ctx context.Context) ([]peerpay.Incoming, []Failure, error) {
	var parsed []peerpay.Incoming
	var failed []Failure
	offset := 0
	for page := 0; page < 50; page++ {
		body, err := json.Marshal(listRequest{MessageBox: c.Box, Limit: 100, Offset: offset})
		if err != nil {
			return nil, nil, err
		}
		status, buf, err := c.Poster.Post(ctx, c.endpoint("/listMessages"), body)
		if err != nil {
			return nil, nil, err
		}
		var resp listResponse
		if err := json.Unmarshal(buf, &resp); err != nil {
			return nil, nil, fmt.Errorf("listMessages returned %d and unreadable JSON", status)
		}
		if status != http.StatusOK || resp.Status == "error" {
			return nil, nil, fmt.Errorf("listMessages %d %s", status, truncate(resp.Code+": "+resp.Description, 180))
		}
		for _, msg := range resp.Messages {
			plain, err := c.plaintextBody(ctx, msg.Sender, msg.Body)
			if err != nil {
				if c.Log != nil {
					c.Log.Warn("message box body", "message_id", msg.MessageID, "error", truncate(err.Error(), 200))
				}
				failed = append(failed, Failure{MessageID: msg.MessageID, Sender: msg.Sender, Err: err})
				continue
			}
			in, err := peerpay.ParseBody(msg.MessageID, msg.Sender, plain)
			if err != nil {
				if c.Log != nil {
					c.Log.Warn("peerpay token", "message_id", msg.MessageID, "error", truncate(err.Error(), 200))
				}
				failed = append(failed, Failure{MessageID: msg.MessageID, Sender: msg.Sender, Err: err})
				continue
			}
			parsed = append(parsed, in)
		}
		if !resp.HasMore || len(resp.Messages) == 0 {
			break
		}
		if resp.NextOffset <= offset {
			break
		}
		offset = resp.NextOffset
	}
	return parsed, failed, nil
}

// Failure is a message the sweeper must not acknowledge.
type Failure struct {
	MessageID string
	Sender    string
	Err       error
}

// Acknowledge deletes messages after the wallet has accepted them.
func (c *Client) Acknowledge(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	body, err := json.Marshal(struct {
		MessageIDs []string `json:"messageIds"`
	}{MessageIDs: ids})
	if err != nil {
		return err
	}
	status, buf, err := c.Poster.Post(ctx, c.endpoint("/acknowledgeMessage"), body)
	if err != nil {
		return err
	}
	var resp listResponse
	_ = json.Unmarshal(buf, &resp)
	if status != http.StatusOK || resp.Status == "error" {
		return fmt.Errorf("acknowledgeMessage %d %s", status, truncate(resp.Code+": "+resp.Description, 180))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}


// plaintextBody unwraps a JSON string and decrypts a BRC-2 Message Box envelope
// ({"encryptedMessage":"<base64>"}). PeerPay's TypeScript client encrypts with
// protocol [1, "messagebox"] and keyID "1" against the recipient identity.
// Without this step the sweeper treats live inbox payments as unreadable tokens.
func (c *Client) plaintextBody(ctx context.Context, sender string, body []byte) ([]byte, error) {
	body = unwrapContent(body)
	var env struct {
		EncryptedMessage string `json:"encryptedMessage"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.EncryptedMessage == "" {
		return body, nil
	}
	if c.Wallet == nil {
		return nil, fmt.Errorf("message is encrypted and no wallet is configured to decrypt it")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.EncryptedMessage)
	if err != nil || len(ciphertext) == 0 {
		return nil, fmt.Errorf("encryptedMessage is not base64")
	}
	senderKey, err := ec.PublicKeyFromString(sender)
	if err != nil {
		return nil, fmt.Errorf("sender is not an identity key")
	}
	res, err := c.Wallet.Decrypt(ctx, sdk.DecryptArgs{
		EncryptionArgs: sdk.EncryptionArgs{
			ProtocolID: sdk.Protocol{SecurityLevel: sdk.SecurityLevelEveryApp, Protocol: "messagebox"},
			KeyID:      "1",
			Counterparty: sdk.Counterparty{
				Type:         sdk.CounterpartyTypeOther,
				Counterparty: senderKey,
			},
		},
		Ciphertext: ciphertext,
	}, c.Originator)
	if err != nil {
		return nil, fmt.Errorf("decrypt message box body: %w", err)
	}
	if c.Log != nil {
		c.Log.Info("decrypted message box body", "bytes", len(res.Plaintext))
	}
	return unwrapContent(res.Plaintext), nil
}


// unwrapContent peels JSON-string wrappers and the Message Box stored-message
// envelope {"message": <body>} so the encrypted PeerPay token is reachable.
func unwrapContent(body []byte) []byte {
	for i := 0; i < 8; i++ {
		body = unwrapJSONString(body)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return body
		}
		if _, ok := fields["customInstructions"]; ok {
			return body
		}
		if _, ok := fields["encryptedMessage"]; ok {
			return body
		}
		inner, ok := fields["message"]
		if !ok || len(inner) == 0 {
			return body
		}
		body = inner
	}
	return body
}

func unwrapJSONString(body []byte) []byte {
	var s string
	if err := json.Unmarshal(body, &s); err == nil && len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		return []byte(s)
	}
	return body
}

// Timeout wraps a poster with a default client timeout when using HTTPPoster.
func DefaultTimeout() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
