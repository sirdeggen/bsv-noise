// Package redact keeps secret material out of logged errors.
package redact

import (
	"strings"
)

// Secrets replaces known secret strings with a fixed marker.
type Secrets struct {
	values []string
}

// New builds a redactor. Empty and very short values are ignored so ordinary
// words are not stripped from errors.
func New(values ...string) *Secrets {
	var kept []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < 8 {
			continue
		}
		kept = append(kept, v)
	}
	return &Secrets{values: kept}
}

// String returns s with every known secret removed.
func (s *Secrets) String(in string) string {
	if s == nil || in == "" {
		return in
	}
	out := in
	for _, v := range s.values {
		if strings.Contains(out, v) {
			out = strings.ReplaceAll(out, v, "[redacted]")
		}
	}
	return out
}

// Error redacts err. A nil error returns an empty string.
func (s *Secrets) Error(err error) string {
	if err == nil {
		return ""
	}
	return s.String(err.Error())
}
