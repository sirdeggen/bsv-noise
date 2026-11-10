// Package arcadestatus maps Arcade lifecycle statuses used to free MaxInFlight
// slots. The raw txStatus string comes from SSE /events frames or GET /tx/:txid.
package arcadestatus

import "strings"

// Event is one transaction lifecycle update.
type Event struct {
	TxID   string
	Status string
	RawTx  string // EF or raw hex from GET /tx, used to rebroadcast a parked tx
}

// FreesInFlight reports whether Arcade has observed the transaction on the
// network well enough to release a MaxInFlight slot. RECEIVED, SENT_TO_NETWORK
// and ACCEPTED_BY_NETWORK are not enough; SEEN_ON_NETWORK and later lifecycle
// statuses are. MINED and IMMUTABLE also free the slot; confirmation with a
// merkle path is a separate step.
func FreesInFlight(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SEEN_ON_NETWORK",
		"SEEN_MULTIPLE_NODES",
		"SEEN_ON_MULTIPLE_NODES",
		"STUMP_PROCESSING",
		"MINED",
		"IMMUTABLE":
		return true
	default:
		return false
	}
}

// IsRejected reports a terminal broadcast failure that should mark the
// payment failed and free its in-flight slot.
func IsRejected(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "REJECTED", "DOUBLE_SPEND_ATTEMPTED":
		return true
	default:
		return false
	}
}

// IsPendingRetry reports that Arcade accepted the payload but parked it for a
// later rebroadcast (for example a merkle-service /watch failure). This does
// not mean the transaction is on the network, and it is not a rejection.
func IsPendingRetry(status string) bool {
	return strings.ToUpper(strings.TrimSpace(status)) == "PENDING_RETRY"
}

func statusRank(status string) int {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "IMMUTABLE":
		return 80
	case "MINED":
		return 70
	case "SEEN_MULTIPLE_NODES", "SEEN_ON_MULTIPLE_NODES":
		return 60
	case "SEEN_ON_NETWORK":
		return 50
	case "STUMP_PROCESSING":
		return 40
	case "ACCEPTED_BY_NETWORK":
		return 30
	case "SENT_TO_NETWORK", "RECEIVED", "ANNOUNCED_TO_NETWORK", "STORED":
		return 20
	case "PENDING_RETRY":
		return 10
	case "REJECTED", "DOUBLE_SPEND_ATTEMPTED":
		return 5
	default:
		return 0
	}
}

// Prefer picks the most advanced lifecycle status for one txid. A SEEN_* (or
// later) report from any Arcade wins over another host still returning
// PENDING_RETRY.
func Prefer(events []Event) []Event {
	best := make(map[string]Event)
	order := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.TxID == "" || ev.Status == "" {
			continue
		}
		prev, ok := best[ev.TxID]
		if !ok {
			best[ev.TxID] = ev
			order = append(order, ev.TxID)
			continue
		}
		if statusRank(ev.Status) > statusRank(prev.Status) {
			if ev.RawTx == "" {
				ev.RawTx = prev.RawTx
			}
			best[ev.TxID] = ev
		} else if prev.RawTx == "" && ev.RawTx != "" {
			prev.RawTx = ev.RawTx
			best[ev.TxID] = prev
		}
	}
	out := make([]Event, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	return out
}
