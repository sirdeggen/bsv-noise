package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sampleStats() Stats {
	return Stats{
		Synthetic:       true,
		Health:          "ok",
		Ready:           true,
		Mode:            "dry-run",
		Network:         "test",
		DryRun:          true,
		IdentityKey:     "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MessageBox:      "payment_inbox",
		MessageBoxURL:   "https://messagebox.bsvblockchain.tech",
		Originator:      "brc29-synthetic-load.local",
		BalanceSatoshis: 100000,
		BalanceBSV:      "0.00100000",
		Transactions:    TransactionCounts{Total: 2, Broadcast: 1, DryRun: 1, Confirmed: 1},
		TPS:             TPS{Target: 0.5, Accepted: 0.2, State: "tracking", ExponentialVarianceSeconds2: 4},
		Funding:         FundingView{Pending: 1},
		Recent: []RecentTx{{
			ID: "p-1", TxID: "sim:p-1", Amount: 100, State: "confirmed", DryRun: true,
			Created: time.Unix(1, 0).UTC(),
		}},
		Note: "A trickle of ordinary-looking payments so real commerce is a needle in a haystack.",
	}
}

func TestStatsArePublicAndDoNotCarrySecrets(t *testing.T) {
	srv := httptest.NewServer((&Server{
		Status:  func() Status { return Status{Synthetic: true, Status: "ok"} },
		Metrics: func() string { return "metric 1\n" },
		Stats:   func() (Stats, error) { return sampleStats(), nil },
		Token:   "metrics-secret",
	}).Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stats status %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "server_private_key", "wif", "arc_token", "console_token", "metrics_token"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("stats exposed %s", forbidden)
		}
	}
	if raw["identity_key"] == "" {
		t.Fatal("missing public identity")
	}
	if !strings.Contains(string(body), "dry_run") {
		t.Fatal("missing dry-run count")
	}

	metrics, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer metrics.Body.Close()
	if metrics.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics without token %d", metrics.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-secret")
	okm, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer okm.Body.Close()
	if okm.StatusCode != http.StatusOK {
		t.Fatalf("metrics with token %d", okm.StatusCode)
	}

	page, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	htmlBody, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(htmlBody), `data-console="noise"`) {
		t.Fatal("console html was not served")
	}
}

func TestConsoleTokenGatesPageAndStats(t *testing.T) {
	h := (&Server{
		Stats:        func() (Stats, error) { return sampleStats(), nil },
		ConsoleToken: "console-secret",
		Token:        "metrics-secret",
	}).Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open stats %d", res.StatusCode)
	}

	page, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	htmlBody, _ := io.ReadAll(page.Body)
	if page.StatusCode != http.StatusUnauthorized || !strings.Contains(string(htmlBody), "Console token") {
		t.Fatalf("login page status %d body %s", page.StatusCode, htmlBody)
	}
	if strings.Contains(string(htmlBody), `data-console="noise"`) {
		t.Fatal("gated console leaked the app bundle")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/stats", nil)
	req.Header.Set("Authorization", "Bearer console-secret")
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("bearer stats %d", ok.StatusCode)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	login, err := client.PostForm(srv.URL+"/console/login", map[string][]string{"token": {"console-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer login.Body.Close()
	if login.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status %d", login.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range login.Cookies() {
		if c.Name == "console_token" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("login did not set an http-only cookie")
	}
	pageReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/console", nil)
	pageReq.AddCookie(cookie)
	opened, err := http.DefaultClient.Do(pageReq)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Body.Close()
	openedBody, _ := io.ReadAll(opened.Body)
	if opened.StatusCode != http.StatusOK || !strings.Contains(string(openedBody), `data-console="noise"`) {
		t.Fatalf("cookie did not open console: %d", opened.StatusCode)
	}
}

func TestPWAAssetsArePublicEvenWithConsoleToken(t *testing.T) {
	srv := httptest.NewServer((&Server{ConsoleToken: "console-secret"}).Handler())
	defer srv.Close()
	want := map[string]string{
		"/manifest.webmanifest": "application/manifest+json",
		"/sw.js":                "text/javascript; charset=utf-8",
		"/icon.svg":             "image/svg+xml",
		"/icon-192.png":         "image/png",
		"/icon-512.png":         "image/png",
	}
	for p, ctype := range want {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("%s: status %d, %d bytes", p, resp.StatusCode, len(body))
		}
		if got := resp.Header.Get("Content-Type"); got != ctype {
			t.Fatalf("%s: content-type %q", p, got)
		}
	}
}

func TestOGImageIsAbsoluteAndHostIsSanitised(t *testing.T) {
	h := (&Server{Stats: func() (Stats, error) { return sampleStats(), nil }}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "noise.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `content="https://noise.example.com/og.jpg"`) {
		t.Fatal("og:image was not made absolute")
	}

	evil := httptest.NewRequest(http.MethodGet, "/", nil)
	evil.Host = `x"><script>alert(1)</script>`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, evil)
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Fatal("host header was reflected into the page")
	}

	gated := (&Server{ConsoleToken: "secret"}).Handler()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "noise.example.com"
	rec = httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `content="http://noise.example.com/og.jpg"`) {
		t.Fatal("login page is missing the absolute og:image")
	}
}
