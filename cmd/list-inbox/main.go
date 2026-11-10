// list-inbox is a read-only Message Box inspector.
// It authenticates as the server identity, lists configured boxes, decrypts
// BRC-2 envelopes, parses PeerPay tokens, and checks BRC-29 locking scripts.
// It never acknowledges, deletes, internalizes, or broadcasts.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/sirdeggen/bsv-noise/internal/brc29pay"
	"github.com/sirdeggen/bsv-noise/internal/messagebox"
	"github.com/sirdeggen/bsv-noise/internal/peerpay"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	keyFile := getenv("SERVER_PRIVATE_KEY_FILE", "secrets/server_private_key")
	base := getenv("MESSAGE_BOX_URL", "https://messagebox.bsvblockchain.tech")
	buf, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("cannot read key file")
	}
	raw := strings.TrimSpace(string(buf))
	key, err := ec.PrivateKeyFromHex(raw)
	if err != nil || len(key.Serialize()) != 32 {
		return fmt.Errorf("key file is not a 32-byte hex private key")
	}
	identity := key.PubKey().ToDERHex()
	fmt.Printf("identity %s\n", identity)
	if expect := strings.TrimSpace(os.Getenv("EXPECT_IDENTITY")); expect != "" {
		if !strings.EqualFold(identity, expect) {
			fmt.Printf("WARN identity does not match EXPECT_IDENTITY\n")
		} else {
			fmt.Printf("identity matches EXPECT_IDENTITY\n")
		}
	}

	wallet, err := sdk.NewCompletedProtoWallet(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	boxes := []string{"payment_inbox", "inbox", "notifications", "payments", "payment_requests", "payment_request_responses"}
	if extra := strings.TrimSpace(os.Getenv("MESSAGE_BOX")); extra != "" {
		boxes = append([]string{extra}, boxes...)
	}

	// Probe a couple of box-directory endpoints without failing the run.
	probeBoxDirectory(ctx, wallet, base)
	dumpRaw(ctx, wallet, base, "payment_inbox")

	seen := map[string]bool{}
	for _, box := range boxes {
		if seen[box] {
			continue
		}
		seen[box] = true
		if err := listBox(ctx, wallet, key, base, box); err != nil {
			fmt.Printf("box %s error: %s\n", box, err)
		}
	}
	fmt.Println("done (read-only; no acknowledge, no internalize)")
	return nil
}

func probeBoxDirectory(ctx context.Context, wallet sdk.Interface, base string) {
	poster := messagebox.NewAuthPoster(wallet)
	for _, path := range []string{"/listUserMessageBoxes", "/listMessageBoxes", "/listBoxes"} {
		status, buf, err := poster.Post(ctx, strings.TrimRight(base, "/")+path, []byte(`{}`))
		if err != nil {
			fmt.Printf("probe %s error: %s\n", path, truncate(err.Error(), 180))
			continue
		}
		fmt.Printf("probe %s status=%d body=%s\n", path, status, truncate(string(buf), 400))
	}
}

func listBox(ctx context.Context, wallet sdk.Interface, key *ec.PrivateKey, base, box string) error {
	c := &messagebox.Client{
		BaseURL: base,
		Box:     box,
		Poster:  messagebox.NewAuthPoster(wallet),
		Wallet:  wallet,
	}
	// Use ListPayments for payment parsing, but also raw-list so we report
	// body shapes even when ParseBody fails after decrypt. ListPayments already
	// decrypts; for body-kind we re-list with a thin raw helper via payments
	// plus failures.
	parsed, failed, err := c.ListPayments(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("box %s parsed=%d failed=%d\n", box, len(parsed), len(failed))
	for _, in := range parsed {
		scriptOK, scriptHex, scriptErr := brc29Match(key, in)
		outScript, _ := outputScriptHex(in.Tx, in.OutputIndex)
		fmt.Printf("  payment message_id=%s sender=%s txid=%s vout=%d amount_token=%d satoshis=%d prefix=%s suffix=%s brc29_derivable=%v locking_script=%s output_script=%s",
			in.MessageID, in.Sender, in.TxID, in.OutputIndex, in.Amount, in.Satoshis, in.Prefix, in.Suffix, scriptOK, scriptHex, outScript)
		if scriptErr != "" {
			fmt.Printf(" brc29_error=%s", scriptErr)
		}
		fmt.Println()
	}
	for _, f := range failed {
		fmt.Printf("  unreadable message_id=%s sender=%s error=%s\n", f.MessageID, f.Sender, truncate(f.Err.Error(), 240))
	}
	return nil
}

func brc29Match(recipient *ec.PrivateKey, in peerpay.Incoming) (bool, string, string) {
	script, err := brc29pay.SameScript(in.SenderKey, recipient, in.Prefix, in.Suffix, true)
	if err != nil {
		return false, "", err.Error()
	}
	hexScript := hex.EncodeToString(script)
	outHex, err := outputScriptHex(in.Tx, in.OutputIndex)
	if err != nil {
		return false, hexScript, err.Error()
	}
	return strings.EqualFold(hexScript, outHex), hexScript, ""
}

func outputScriptHex(raw []byte, index uint32) (string, error) {
	beef, txHash, err := transaction.NewBeefFromAtomicBytes(raw)
	if err != nil {
		return "", err
	}
	tx := beef.FindAtomicTransactionByHash(txHash)
	if tx == nil || int(index) >= len(tx.Outputs) {
		return "", fmt.Errorf("no output")
	}
	if tx.Outputs[index].LockingScript == nil {
		return "", fmt.Errorf("no locking script")
	}
	return tx.Outputs[index].LockingScript.String(), nil
}


func dumpRaw(ctx context.Context, wallet sdk.Interface, base, box string) {
	poster := messagebox.NewAuthPoster(wallet)
	req, _ := json.Marshal(map[string]any{"messageBox": box, "limit": 100, "offset": 0})
	status, buf, err := poster.Post(ctx, strings.TrimRight(base, "/")+"/listMessages", req)
	if err != nil {
		fmt.Printf("raw list %s error: %s\n", box, err)
		return
	}
	var top any
	if err := json.Unmarshal(buf, &top); err != nil {
		fmt.Printf("raw list %s status=%d unreadable json bytes=%d\n", box, status, len(buf))
		return
	}
	sum, _ := json.MarshalIndent(summarize(top), "", "  ")
	fmt.Printf("raw list %s status=%d bytes=%d\n%s\n", box, status, len(buf), sum)
}

func summarize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			lk := strings.ToLower(k)
			if lk == "encryptedmessage" || strings.Contains(lk, "ciphertext") || strings.Contains(lk, "beef") {
				if s, ok := val.(string); ok {
					out[k] = fmt.Sprintf("string:%d", len(s))
					continue
				}
			}
			out[k] = summarize(val)
		}
		return out
	case []any:
		items := make([]any, 0, len(t))
		for i, item := range t {
			if i >= 8 {
				items = append(items, fmt.Sprintf("… %d more", len(t)-8))
				break
			}
			items = append(items, summarize(item))
		}
		return items
	case string:
		s := t
		if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
			var inner any
			if err := json.Unmarshal([]byte(s), &inner); err == nil {
				return map[string]any{"json_string": summarize(inner), "len": len(s)}
			}
		}
		if len(s) > 96 {
			return fmt.Sprintf("string:%d head=%q", len(s), s[:24])
		}
		return s
	case json.Number:
		return t.String()
	default:
		return t
	}
}

func getenv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
