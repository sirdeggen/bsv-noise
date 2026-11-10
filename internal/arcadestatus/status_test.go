package arcadestatus

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFreesInFlightStatuses(t *testing.T) {
	yes := []string{"SEEN_ON_NETWORK", "SEEN_MULTIPLE_NODES", "SEEN_ON_MULTIPLE_NODES", "STUMP_PROCESSING", "MINED", "IMMUTABLE", "seen_on_network"}
	no := []string{"", "RECEIVED", "SENT_TO_NETWORK", "ACCEPTED_BY_NETWORK", "PENDING_RETRY", "UNKNOWN", "REJECTED", "DOUBLE_SPEND_ATTEMPTED"}
	for _, s := range yes {
		if !FreesInFlight(s) {
			t.Errorf("%s should free an in-flight slot", s)
		}
	}
	for _, s := range no {
		if FreesInFlight(s) {
			t.Errorf("%s must not free an in-flight slot", s)
		}
	}
}

func TestIsRejected(t *testing.T) {
	if !IsRejected("REJECTED") || !IsRejected("DOUBLE_SPEND_ATTEMPTED") {
		t.Fatal("expected rejected statuses")
	}
	if IsRejected("SEEN_ON_NETWORK") || IsRejected("MINED") || IsRejected("") {
		t.Fatal("did not expect reject")
	}
}

func TestClientQueryParsesTxStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tx/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/tx/")
		switch id {
		case "seen-tx":
			io.WriteString(w, `{"txid":"seen-tx","txStatus":"SEEN_ON_NETWORK"}`)
		case "mined-tx":
			io.WriteString(w, `{"txid":"mined-tx","txStatus":"MINED"}`)
		case "early-tx":
			io.WriteString(w, `{"txid":"early-tx","txStatus":"ACCEPTED_BY_NETWORK"}`)
		case "rej-tx":
			io.WriteString(w, `{"txid":"rej-tx","txStatus":"REJECTED"}`)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	got, err := c.Query(context.Background(), []string{"seen-tx", "early-tx", "mined-tx", "rej-tx", "missing-tx"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"seen-tx":  "SEEN_ON_NETWORK",
		"early-tx": "ACCEPTED_BY_NETWORK",
		"mined-tx": "MINED",
		"rej-tx":   "REJECTED",
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, ev := range got {
		if want[ev.TxID] != ev.Status {
			t.Errorf("%s: got %s want %s", ev.TxID, ev.Status, want[ev.TxID])
		}
		if ev.TxID == "early-tx" && FreesInFlight(ev.Status) {
			t.Error("ACCEPTED_BY_NETWORK must not free a slot")
		}
	}
}

func TestPreferPicksSeenOverPendingRetry(t *testing.T) {
	got := Prefer([]Event{
		{TxID: "aa", Status: "PENDING_RETRY", RawTx: "dead"},
		{TxID: "aa", Status: "SEEN_MULTIPLE_NODES"},
		{TxID: "bb", Status: "ACCEPTED_BY_NETWORK"},
	})
	if len(got) != 2 {
		t.Fatalf("len %d", len(got))
	}
	if got[0].TxID != "aa" || got[0].Status != "SEEN_MULTIPLE_NODES" || got[0].RawTx != "dead" {
		t.Fatalf("aa %+v", got[0])
	}
	if !FreesInFlight(got[0].Status) {
		t.Fatal("preferred status should free the slot")
	}
}

func TestIsPendingRetry(t *testing.T) {
	if !IsPendingRetry("PENDING_RETRY") || IsPendingRetry("SEEN_ON_NETWORK") {
		t.Fatal("pending retry mapping")
	}
}

func TestQueryPrefersFallbackSeenOverPendingRetry(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"txid":"stuck-tx","txStatus":"PENDING_RETRY","rawTx":"0a0b"}`)
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"txid":"stuck-tx","txStatus":"SEEN_MULTIPLE_NODES"}`)
	}))
	t.Cleanup(fallback.Close)
	c := &Client{BaseURL: primary.URL, FallbackURLs: []string{fallback.URL}, HTTP: primary.Client()}
	got, err := c.Query(context.Background(), []string{"stuck-tx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != "SEEN_MULTIPLE_NODES" || got[0].RawTx != "0a0b" {
		t.Fatalf("got %+v", got)
	}
}

func TestBroadcastPostsToEveryHost(t *testing.T) {
	var hits []string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/tx" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("content type %s", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		hits = append(hits, string(body))
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"txStatus":"RECEIVED"}`)
	}))
	t.Cleanup(ok.Close)
	c := &Client{BaseURL: ok.URL, FallbackURLs: []string{ok.URL + "/ignored-dup-of-same-host-trimmed-will-dedupe"}, HTTP: ok.Client()}
	// Use a distinct second server
	ok2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hits = append(hits, "2:"+string(body))
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"txStatus":"RECEIVED"}`)
	}))
	t.Cleanup(ok2.Close)
	c.FallbackURLs = []string{ok2.URL}
	if err := c.BroadcastHex(context.Background(), "0a0b"); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits %v", hits)
	}
}
