package httpserver

import (
	"crypto/subtle"
	_ "embed"
	"html"
	"net/http"
	"time"
)

//go:embed console.html
var consoleHTML []byte

// Stats is the public console document. IdentityKey is a public key.
// Private keys are never included.
type Stats struct {
	Synthetic       bool              `json:"synthetic"`
	Health          string            `json:"health"`
	Ready           bool              `json:"ready"`
	Paused          string            `json:"paused,omitempty"`
	Mode            string            `json:"mode"`
	Network         string            `json:"network"`
	DryRun          bool              `json:"dry_run"`
	Broadcast       bool              `json:"broadcast"`
	ImportFunding   bool              `json:"import_funding"`
	IdentityKey     string            `json:"identity_key"`
	MessageBox      string            `json:"message_box"`
	MessageBoxURL   string            `json:"message_box_url"`
	Originator      string            `json:"originator"`
	BalanceSatoshis int64             `json:"balance_satoshis"`
	BalanceBSV      string            `json:"balance_bsv"`
	Transactions    TransactionCounts `json:"transactions"`
	TPS             TPS               `json:"tps"`
	Funding         FundingView       `json:"funding"`
	Recent          []RecentTx        `json:"recent"`
	Note            string            `json:"note"`
}

// TransactionCounts splits made payments into broadcast and dry-run.
type TransactionCounts struct {
	Total     int64 `json:"total"`
	Broadcast int64 `json:"broadcast"`
	DryRun    int64 `json:"dry_run"`
	Confirmed int64 `json:"confirmed"`
	Seen      int64 `json:"seen"`
	Failed    int64 `json:"failed"`
	InFlight  int64 `json:"in_flight"`
	DustSwept int   `json:"dust_swept"`
}

// TPS is the recent rate and the variance of interarrival gaps.
type TPS struct {
	Target                      float64 `json:"target"`
	Attempted                   float64 `json:"attempted"`
	Accepted                    float64 `json:"accepted"`
	Confirmed                   float64 `json:"confirmed"`
	Failed                      float64 `json:"failed"`
	IntervalVarianceSeconds2    float64 `json:"interval_variance_seconds2"`
	ExponentialVarianceSeconds2 float64 `json:"exponential_variance_seconds2"`
	SampleGaps                  int     `json:"sample_gaps"`
	State                       string  `json:"state"`
}

// FundingView is the Message Box sweep snapshot.
type FundingView struct {
	Pending      int64      `json:"pending"`
	Acknowledged int64      `json:"acknowledged"`
	Failed       int64      `json:"failed"`
	LastSweep    *time.Time `json:"last_sweep"`
	LastError    string     `json:"last_error,omitempty"`
}

// RecentTx is one generator payment. ExplorerURL is empty for simulated ids.
type RecentTx struct {
	ID          string    `json:"id"`
	TxID        string    `json:"txid"`
	Amount      int64     `json:"amount_satoshis"`
	Fee         int64     `json:"fee_satoshis"`
	State       string    `json:"state"`
	DryRun      bool      `json:"dry_run"`
	ExplorerURL string    `json:"explorer_url,omitempty"`
	Created     time.Time `json:"created"`
}

func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	if !s.consoleAuthorized(r) {
		s.loginPage(w, "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(consoleHTML)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if !s.consoleAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"status": "unauthorized"})
		return
	}
	if s.Stats == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	doc, err := s.Stats()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error"})
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.ConsoleToken == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.loginPage(w, "The form could not be read.")
		return
	}
	if !tokenMatch(r.PostFormValue("token"), s.ConsoleToken) {
		s.loginPage(w, "That token was not accepted.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "console_token",
		Value:    s.ConsoleToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   12 * 60 * 60,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) consoleAuthorized(r *http.Request) bool {
	if s.ConsoleToken == "" {
		return true
	}
	if tokenMatch(bearer(r), s.ConsoleToken) {
		return true
	}
	cookie, err := r.Cookie("console_token")
	if err != nil {
		return false
	}
	return tokenMatch(cookie.Value, s.ConsoleToken)
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || h[:len(prefix)] != prefix {
		return ""
	}
	return h[len(prefix):]
}

func tokenMatch(got, want string) bool {
	if want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) loginPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	msg := ""
	if message != "" {
		msg = "<p>" + html.EscapeString(message) + "</p>"
	}
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Console token</title></head><body>
<h1>Console token</h1>` + msg + `
<form method="post" action="/console/login"><label>Token <input name="token" type="password" autocomplete="current-password"></label>
<button type="submit">Open console</button></form></body></html>`))
}
