package arcadestatus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// QueryWhatsOnChain looks up txids on WhatsOnChain when Arcade does not know
// them. A transaction that WoC has already indexed is treated as seen on the
// network (MINED if it has a block, otherwise SEEN_ON_NETWORK) so MaxInFlight
// can free even when Arcade returns 404 for a mined payment.
func QueryWhatsOnChain(ctx context.Context, network string, txids []string, httpc *http.Client) ([]Event, error) {
	return queryWhatsOnChainAt(ctx, whatsOnChainBase(network), txids, httpc)
}

func whatsOnChainBase(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "main", "mainnet":
		return "https://api.whatsonchain.com/v1/bsv/main"
	default:
		return "https://api.whatsonchain.com/v1/bsv/test"
	}
}

type wocTx struct {
	TxID          string `json:"txid"`
	Confirmations int    `json:"confirmations"`
	Blockhash     string `json:"blockhash"`
}

func queryWhatsOnChainAt(ctx context.Context, base string, txids []string, httpc *http.Client) ([]Event, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("whatsonchain status client has no base URL")
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: 10 * time.Second}
	}
	var out []Event
	var firstErr error
	for _, txid := range txids {
		txid = strings.TrimSpace(txid)
		if txid == "" {
			continue
		}
		ev, err := getWhatsOnChain(ctx, httpc, base, txid)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ev != nil {
			out = append(out, *ev)
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func getWhatsOnChain(ctx context.Context, httpc *http.Client, base, txid string) (*Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/tx/"+txid, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "brc29-console")
	res, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whatsonchain GET /tx/%s: HTTP %d", txid, res.StatusCode)
	}
	var info wocTx
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if info.TxID == "" {
		info.TxID = txid
	}
	status := "SEEN_ON_NETWORK"
	if info.Confirmations > 0 || strings.TrimSpace(info.Blockhash) != "" {
		status = "MINED"
	}
	return &Event{TxID: info.TxID, Status: status}, nil
}
