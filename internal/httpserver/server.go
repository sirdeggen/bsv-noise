// Package httpserver serves liveness, readiness and metrics.
// There is no admin mutation route.
package httpserver

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"
)

// Status is the public health document. IdentityKey is a public key.
type Status struct {
	Status        string `json:"status"`
	Synthetic     bool   `json:"synthetic"`
	Mode          string `json:"mode"`
	Network       string `json:"network"`
	IdentityKey   string `json:"identity_key"`
	MessageBox    string `json:"message_box"`
	MessageBoxURL string `json:"message_box_url"`
	Broadcast     bool   `json:"broadcast"`
	ImportFunding bool   `json:"import_funding"`
	Ready         bool   `json:"ready"`
	Paused        string `json:"paused,omitempty"`
	Note          string `json:"note"`
	Toolbox       string `json:"wallet_toolbox"`
	GoSDK         string `json:"go_sdk"`
}

// Server is the read-only HTTP process.
type Server struct {
	Status       func() Status
	Metrics      func() string
	Stats        func() (Stats, error)
	Token        string
	ConsoleToken string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.console)
	mux.HandleFunc("GET /console", s.console)
	mux.HandleFunc("POST /console/login", s.login)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	st := s.Status()
	st.Status = "ok"
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	st := s.Status()
	code := http.StatusOK
	if !st.Ready {
		code = http.StatusServiceUnavailable
		st.Status = "paused"
	} else {
		st.Status = "ready"
	}
	writeJSON(w, code, st)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(s.Metrics()))
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.Token
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

// Listen starts the server and closes it when the process asks.
func Listen(addr string, h http.Handler) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// Loopback reports whether addr binds only to loopback.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() || host == "localhost"
}

// WarnOpen is true when metrics have no token and the listener is not loopback.
func WarnOpen(addr, token string) bool {
	return strings.TrimSpace(token) == "" && !Loopback(addr)
}
