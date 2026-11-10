package arcadestatus

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client queries Arcade's GET /tx/:txid endpoint for a raw txStatus. This is
// the fallback when an SSE frame has not yet arrived. GetStatusForTxIDs is
// not used here because it collapses RECEIVED and SEEN_ON_NETWORK into the
// same "known" bucket, which would free a slot too early.
//
// FallbackURLs are queried when the primary is missing the tx or reports a
// less advanced status (notably PENDING_RETRY while another Arcade has already
// seen the transaction on the network).
type Client struct {
	BaseURL      string
	FallbackURLs []string
	HTTP         *http.Client
}

type txInfo struct {
	TxID     string `json:"txid"`
	TxStatus string `json:"txStatus"`
	RawTx    string `json:"rawTx"`
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *Client) bases() []string {
	if c == nil {
		return nil
	}
	var out []string
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" {
			return
		}
		for _, have := range out {
			if have == u {
				return
			}
		}
		out = append(out, u)
	}
	add(c.BaseURL)
	for _, u := range c.FallbackURLs {
		add(u)
	}
	return out
}

// Query looks up each txid across the primary and fallback Arcade hosts and
// returns the most advanced status per txid. Missing transactions are omitted
// so the caller can retry later.
func (c *Client) Query(ctx context.Context, txids []string) ([]Event, error) {
	bases := c.bases()
	if len(bases) == 0 {
		return nil, fmt.Errorf("arcade status client has no base URL")
	}
	var out []Event
	var firstErr error
	for _, txid := range txids {
		if txid == "" {
			continue
		}
		var found []Event
		for _, base := range bases {
			ev, err := c.getTx(ctx, base, txid)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if ev != nil {
				found = append(found, *ev)
			}
		}
		out = append(out, Prefer(found)...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (c *Client) getTx(ctx context.Context, base, txid string) (*Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/tx/"+txid, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "brc29-console")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arcade GET /tx/%s: HTTP %d", txid, res.StatusCode)
	}
	var info txInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if info.TxStatus == "" {
		return nil, nil
	}
	if info.TxID == "" {
		info.TxID = txid
	}
	return &Event{TxID: info.TxID, Status: info.TxStatus, RawTx: info.RawTx}, nil
}

// Broadcast posts an extended-format transaction to every configured Arcade
// host. It succeeds if at least one host accepts the payload. The body is the
// raw EF bytes, not hex.
func (c *Client) Broadcast(ctx context.Context, ef []byte) error {
	if len(ef) == 0 {
		return fmt.Errorf("empty transaction")
	}
	bases := c.bases()
	if len(bases) == 0 {
		return fmt.Errorf("arcade status client has no base URL")
	}
	var firstErr error
	accepted := 0
	for _, base := range bases {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/tx", bytes.NewReader(ef))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "brc29-console")
		res, err := c.httpClient().Do(req)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			accepted++
			continue
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("arcade POST /tx: HTTP %d", res.StatusCode)
		}
	}
	if accepted == 0 {
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("arcade POST /tx: no host accepted the transaction")
	}
	return nil
}

// BroadcastHex decodes a hex-encoded EF or raw transaction and broadcasts it.
func (c *Client) BroadcastHex(ctx context.Context, rawHex string) error {
	rawHex = strings.TrimSpace(rawHex)
	if rawHex == "" {
		return fmt.Errorf("empty transaction hex")
	}
	ef, err := hex.DecodeString(rawHex)
	if err != nil {
		return fmt.Errorf("decode transaction hex: %w", err)
	}
	return c.Broadcast(ctx, ef)
}
