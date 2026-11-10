package arcadestatus

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryWhatsOnChainMinedAndMempoolAndMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tx/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/tx/")
		switch id {
		case "mined-tx":
			io.WriteString(w, `{"txid":"mined-tx","confirmations":7,"blockhash":"00ab"}`)
		case "mempool-tx":
			io.WriteString(w, `{"txid":"mempool-tx","confirmations":0,"blockhash":""}`)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	got, err := queryWhatsOnChainAt(context.Background(), srv.URL, []string{"mined-tx", "mempool-tx", "missing-tx"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"mined-tx":    "MINED",
		"mempool-tx":  "SEEN_ON_NETWORK",
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, ev := range got {
		if want[ev.TxID] != ev.Status {
			t.Errorf("%s: got %s want %s", ev.TxID, ev.Status, want[ev.TxID])
		}
		if !FreesInFlight(ev.Status) {
			t.Errorf("%s %s should free in-flight", ev.TxID, ev.Status)
		}
	}
}

func TestWhatsOnChainBase(t *testing.T) {
	if whatsOnChainBase("main") != "https://api.whatsonchain.com/v1/bsv/main" {
		t.Fatal(whatsOnChainBase("main"))
	}
	if whatsOnChainBase("test") != "https://api.whatsonchain.com/v1/bsv/test" {
		t.Fatal(whatsOnChainBase("test"))
	}
}
