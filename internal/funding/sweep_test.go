package funding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/sirdeggen/bsv-noise/internal/messagebox"
	"github.com/sirdeggen/bsv-noise/internal/peerpay"
	"github.com/sirdeggen/bsv-noise/internal/store"
)

type boxMem struct {
	mu   sync.Mutex
	msgs map[string]pending
}

type pending struct {
	sender string
	body   string
}

func TestSweepImportsOnceAndRetriesAck(t *testing.T) {
	sender, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	payee, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := script.NewAddressFromPublicKey(payee.PubKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := p2pkh.Lock(addr)
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTransaction()
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 2500, LockingScript: lock})
	beef := transaction.NewBeef()
	if _, err := beef.MergeTransaction(tx); err != nil {
		t.Fatal(err)
	}
	raw, err := beef.AtomicBytes(tx.TxID())
	if err != nil {
		t.Fatal(err)
	}
	nums := make([]int, len(raw))
	for i, b := range raw {
		nums[i] = int(b)
	}
	token, err := json.Marshal(map[string]any{
		"customInstructions": map[string]string{
			"derivationPrefix": base64.StdEncoding.EncodeToString([]byte("pref")),
			"derivationSuffix": base64.StdEncoding.EncodeToString([]byte("suff")),
		},
		"transaction": nums,
		"amount":      2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := &boxMem{msgs: map[string]pending{
		"m1": {sender: sender.PubKey().ToDERHex(), body: string(token)},
	}}
	var acks int
	failAck := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mem.mu.Lock()
		defer mem.mu.Unlock()
		switch r.URL.Path {
		case "/listMessages":
			var msgs []map[string]string
			for id, m := range mem.msgs {
				msgs = append(msgs, map[string]string{"messageId": id, "sender": m.sender, "body": m.body})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "messages": msgs, "hasMore": false})
		case "/acknowledgeMessage":
			acks++
			if failAck {
				failAck = false
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "code": "ERR_INTERNAL_ERROR", "description": "temporary"})
				return
			}
			var req struct {
				MessageIDs []string `json:"messageIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			for _, id := range req.MessageIDs {
				delete(mem.msgs, id)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "loadgen.sqlite")
	st, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnsureGenesis("sim:genesis:0", 1000); err != nil {
		t.Fatal(err)
	}
	box := &messagebox.Client{BaseURL: srv.URL, Box: "payment_inbox", Poster: messagebox.HTTPPoster{Client: srv.Client()}}
	calls := 0
	imp := importerFunc(func(ctx context.Context, in peerpay.Incoming) (bool, error) {
		calls++
		if in.Satoshis != 2500 || in.MessageID != "m1" {
			t.Errorf("import %+v", in)
		}
		return true, nil
	})
	first, err := SweepOnce(context.Background(), box, st, imp, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 1 || first.Pending != 1 || first.Acked != 0 {
		t.Fatalf("first %+v", first)
	}
	free, _, err := st.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free != 3500 {
		t.Fatalf("balance %d", free)
	}
	second, err := SweepOnce(context.Background(), box, st, imp, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Acked != 1 {
		t.Fatalf("second %+v", second)
	}
	if calls != 1 {
		t.Fatalf("internalize calls %d", calls)
	}
	free, _, err = st.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free != 3500 {
		t.Fatalf("balance after retry %d", free)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	free, _, err = st.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free != 3500 {
		t.Fatalf("restart balance %d", free)
	}
	got, ok, err := st.FundingState("m1")
	if err != nil || !ok || got.State != store.FundAcknowledged {
		t.Fatalf("funding %+v ok %v err %v", got, ok, err)
	}
}

type importerFunc func(context.Context, peerpay.Incoming) (bool, error)

func (f importerFunc) Internalize(ctx context.Context, in peerpay.Incoming) (bool, error) {
	return f(ctx, in)
}
