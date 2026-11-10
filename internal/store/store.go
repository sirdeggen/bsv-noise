// Package store persists payment intents, funding acknowledgements and the
// dry-run UTXO set in SQLite. A restart reads this file. Broadcast acceptance
// is not confirmation, and a row left in "sending" is never replayed as a
// second broadcast when the process may already have talked to the network.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/ledger"

	_ "modernc.org/sqlite"
)

const (
	StateSending   = "sending"
	StateAccepted  = "accepted"
	StateSeen      = "seen"
	StateConfirmed = "confirmed"
	StateFailed    = "failed"
	StateAborted   = "aborted"
	StateInDoubt   = "in_doubt"

	FundInternalized = "internalized"
	FundAckPending   = "ack_pending"
	FundAcknowledged = "acknowledged"
	FundFailed       = "failed"
)

var (
	ErrInsufficientFunds = errors.New("insufficient funds above the reserve")
	ErrDailyCap          = errors.New("daily spend or fee cap reached")
	ErrInDoubt           = errors.New("an in-doubt payment blocks new sends")
	ErrFeeBound          = errors.New("fee outside configured per-transaction bounds")
	ErrNotFound          = errors.New("payment not found")
	ErrConflict          = errors.New("payment is already finished")
)

// Payment is one generator attempt.
type Payment struct {
	ID            string
	Amount        int64
	Fee           int64
	Recipient     string
	Prefix        string
	Suffix        string
	LockingScript string
	State         string
	TxID          string
	OutputIndex   int
	Error         string
	Created       time.Time
	Accepted      time.Time
	Seen          time.Time
	Confirmed     time.Time
}

// Funding is one Message Box payment the sweeper has seen.
type Funding struct {
	MessageID string
	Sender    string
	TxID      string
	Vout      int
	Satoshis  int64
	State     string
	Error     string
}

// BeginRequest asks the store to reserve a payment.
type BeginRequest struct {
	ID            string
	Amount        int64
	Fee           int64
	Recipient     string
	Prefix        string
	Suffix        string
	LockingScript string
	// Available is the spendable balance the caller observed. Dry-run ignores
	// it and uses the UTXO table. Live mode passes the wallet balance.
	Available     int64
	Live          bool
	Now           time.Time
	Reserve       int64
	DailySpendCap int64
	DailyFeeCap   int64
	MaxInputs     int
}

// Store is safe for concurrent use.
type Store struct {
	mu  sync.Mutex
	db  *sql.DB
	ext bool // externalEffects: crash during sending becomes in_doubt
}

// Open creates or opens the SQLite file. externalEffects selects crash
// recovery: a dry-run reservation is released, a live attempt becomes in doubt.
func Open(path string, externalEffects bool) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, ext: externalEffects}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.recover(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS utxo (
  outpoint TEXT PRIMARY KEY,
  satoshis INTEGER NOT NULL,
  reserved_by TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS reservation (
  payment_id TEXT NOT NULL,
  outpoint TEXT NOT NULL,
  PRIMARY KEY (payment_id, outpoint)
);
CREATE TABLE IF NOT EXISTS payment (
  id TEXT PRIMARY KEY,
  created_unix INTEGER NOT NULL,
  accepted_unix INTEGER NOT NULL DEFAULT 0,
  confirmed_unix INTEGER NOT NULL DEFAULT 0,
  amount INTEGER NOT NULL,
  fee INTEGER NOT NULL,
  recipient TEXT NOT NULL,
  prefix TEXT NOT NULL,
  suffix TEXT NOT NULL,
  locking_script_hex TEXT NOT NULL,
  state TEXT NOT NULL,
  txid TEXT NOT NULL DEFAULT '',
  output_index INTEGER NOT NULL DEFAULT -1,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS funding (
  message_id TEXT PRIMARY KEY,
  sender TEXT NOT NULL,
  txid TEXT NOT NULL,
  vout INTEGER NOT NULL,
  satoshis INTEGER NOT NULL,
  state TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  UNIQUE (txid, vout)
);
CREATE TABLE IF NOT EXISTS daily (
  day TEXT PRIMARY KEY,
  spend INTEGER NOT NULL DEFAULT 0,
  fees INTEGER NOT NULL DEFAULT 0,
  held_spend INTEGER NOT NULL DEFAULT 0,
  held_fees INTEGER NOT NULL DEFAULT 0
);`)
	if err != nil {
		return err
	}
	return s.ensureColumn("payment", "seen_unix", "INTEGER NOT NULL DEFAULT 0")
}

// ensureColumn adds a column to an existing SQLite table when a previous
// schema is being opened. CREATE TABLE IF NOT EXISTS does not alter columns.
func (s *Store) ensureColumn(table, name, decl string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var col, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &col, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if col == name {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + decl)
	return err
}

func (s *Store) recover() error {
	rows, err := s.db.Query(`SELECT id, amount, fee FROM payment WHERE state = ?`, StateSending)
	if err != nil {
		return err
	}
	defer rows.Close()
	type hold struct {
		id     string
		amount int64
		fee    int64
	}
	var pending []hold
	for rows.Next() {
		var h hold
		if err := rows.Scan(&h.id, &h.amount, &h.fee); err != nil {
			return err
		}
		pending = append(pending, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range pending {
		if s.ext {
			if err := s.markInDoubt(h.id, h.amount, h.fee); err != nil {
				return err
			}
			continue
		}
		if err := s.abortLocked(h.id, "released after restart before commit"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) markInDoubt(id string, amount, fee int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE payment SET state = ?, error = ? WHERE id = ? AND state = ?`,
		StateInDoubt, "restarted while the wallet call was in flight", id, StateSending); err != nil {
		return err
	}
	day := time.Now().UTC().Format("2006-01-02")
	if _, err := tx.Exec(`INSERT INTO daily(day, spend, fees, held_spend, held_fees) VALUES(?,0,0,?,?)
		ON CONFLICT(day) DO UPDATE SET held_spend = held_spend + excluded.held_spend, held_fees = held_fees + excluded.held_fees`,
		day, amount, fee); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM reservation WHERE payment_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE utxo SET reserved_by = '' WHERE reserved_by = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureGenesis inserts the dry-run starting output once per database.
func (s *Store) EnsureGenesis(outpoint string, satoshis int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'genesis'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if satoshis > 0 {
		if _, err := tx.Exec(`INSERT INTO utxo(outpoint, satoshis, reserved_by) VALUES(?,?, '')`, outpoint, satoshis); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('genesis', '1')`); err != nil {
		return err
	}
	return tx.Commit()
}

// NextID returns a monotonic payment id that survives restarts.
func (s *Store) NextID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var raw sql.NullString
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'pay_seq'`).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	seq := int64(1)
	if raw.Valid {
		if _, err := fmt.Sscan(raw.String, &seq); err != nil {
			return "", err
		}
		seq++
	}
	id := fmt.Sprintf("p-%d", seq)
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('pay_seq', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprintf("%d", seq)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// InDoubt reports how many payments cannot be retried safely.
func (s *Store) InDoubt() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM payment WHERE state = ?`, StateInDoubt).Scan(&n)
	return n, err
}

// Get returns a payment by id.
func (s *Store) Get(id string) (Payment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(id)
}

func (s *Store) get(id string) (Payment, bool, error) {
	var p Payment
	var created, accepted, seen, confirmed int64
	err := s.db.QueryRow(`SELECT id, amount, fee, recipient, prefix, suffix, locking_script_hex, state, txid, output_index, error, created_unix, accepted_unix, COALESCE(seen_unix,0), confirmed_unix
		FROM payment WHERE id = ?`, id).Scan(
		&p.ID, &p.Amount, &p.Fee, &p.Recipient, &p.Prefix, &p.Suffix, &p.LockingScript, &p.State, &p.TxID, &p.OutputIndex, &p.Error,
		&created, &accepted, &seen, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, err
	}
	p.Created = time.Unix(created, 0).UTC()
	if accepted > 0 {
		p.Accepted = time.Unix(accepted, 0).UTC()
	}
	if seen > 0 {
		p.Seen = time.Unix(seen, 0).UTC()
	}
	if confirmed > 0 {
		p.Confirmed = time.Unix(confirmed, 0).UTC()
	}
	return p, true, nil
}

// Begin reserves inputs (dry-run) or records a live attempt. Calling Begin
// again with the same id is idempotent: an accepted payment is returned via
// ErrConflict's absence — the caller should Get first. A second Begin of an
// in-flight id returns ErrInDoubt.
func (s *Store) Begin(req BeginRequest) (Payment, error) {
	if req.Amount <= 0 || req.Fee < 0 {
		return Payment{}, errors.New("amount must be positive and fee non-negative")
	}
	if req.ID == "" {
		return Payment{}, errors.New("payment id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok, err := s.get(req.ID); err != nil {
		return Payment{}, err
	} else if ok {
		switch existing.State {
		case StateAccepted, StateSeen, StateConfirmed:
			return existing, ErrConflict
		case StateSending, StateInDoubt:
			return existing, ErrInDoubt
		default:
			return existing, ErrConflict
		}
	}
	var doubt int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM payment WHERE state = ?`, StateInDoubt).Scan(&doubt); err != nil {
		return Payment{}, err
	}
	if doubt > 0 {
		return Payment{}, ErrInDoubt
	}
	day := req.Now.UTC().Format("2006-01-02")
	spend, fees, heldSpend, heldFees, err := s.daily(day)
	if err != nil {
		return Payment{}, err
	}
	if req.DailySpendCap > 0 && spend+heldSpend+req.Amount > req.DailySpendCap {
		return Payment{}, ErrDailyCap
	}
	if req.DailyFeeCap > 0 && fees+heldFees+req.Fee > req.DailyFeeCap {
		return Payment{}, ErrDailyCap
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Payment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if !req.Live {
		utxos, err := loadUTXOs(tx)
		if err != nil {
			return Payment{}, err
		}
		need := req.Amount + req.Fee + req.Reserve
		// Reserve is a floor on the balance that must remain, not an extra
		// output. Coverage is amount+fee, and the unreserved remainder must
		// stay >= reserve.
		free := int64(0)
		for _, u := range utxos {
			if u.ReservedBy == "" {
				free += u.Satoshis
			}
		}
		if free < req.Amount+req.Fee+req.Reserve {
			return Payment{}, ErrInsufficientFunds
		}
		chosen, err := ledger.Select(utxos, req.Amount+req.Fee, req.MaxInputs)
		if err != nil {
			return Payment{}, ErrInsufficientFunds
		}
		var inputSum int64
		for _, u := range chosen {
			inputSum += u.Satoshis
		}
		if free-(inputSum-(req.Amount+req.Fee)) < req.Reserve && free-req.Amount-req.Fee < req.Reserve {
			return Payment{}, ErrInsufficientFunds
		}
		// After spending, remaining free satoshis are free - inputSum + change,
		// and change = inputSum - amount - fee, so remaining = free - amount - fee.
		_ = need
		for _, u := range chosen {
			res, err := tx.Exec(`UPDATE utxo SET reserved_by = ? WHERE outpoint = ? AND reserved_by = ''`, req.ID, u.Outpoint)
			if err != nil {
				return Payment{}, err
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				return Payment{}, fmt.Errorf("lost reservation race for %s", u.Outpoint)
			}
			if _, err := tx.Exec(`INSERT INTO reservation(payment_id, outpoint) VALUES(?,?)`, req.ID, u.Outpoint); err != nil {
				return Payment{}, err
			}
		}
	} else if req.Available < req.Amount+req.Fee+req.Reserve {
		return Payment{}, ErrInsufficientFunds
	}

	now := req.Now.UTC().Unix()
	if now == 0 {
		now = time.Now().UTC().Unix()
	}
	_, err = tx.Exec(`INSERT INTO payment(id, created_unix, amount, fee, recipient, prefix, suffix, locking_script_hex, state)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		req.ID, now, req.Amount, req.Fee, req.Recipient, req.Prefix, req.Suffix, req.LockingScript, StateSending)
	if err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Payment{}, err
	}
	p, _, err := s.get(req.ID)
	return p, err
}

func (s *Store) daily(day string) (spend, fees, heldSpend, heldFees int64, err error) {
	err = s.db.QueryRow(`SELECT spend, fees, held_spend, held_fees FROM daily WHERE day = ?`, day).
		Scan(&spend, &fees, &heldSpend, &heldFees)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, 0, nil
	}
	return spend, fees, heldSpend, heldFees, err
}

// FinishAccepted marks a reserved payment accepted and, in dry-run, moves
// coins. A second call with the same id is a no-op success when the txid matches.
func (s *Store) FinishAccepted(id, txid string, outputIndex int, when time.Time) (Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok, err := s.get(id)
	if err != nil {
		return Payment{}, err
	}
	if !ok {
		return Payment{}, ErrNotFound
	}
	if existing.State == StateAccepted || existing.State == StateSeen || existing.State == StateConfirmed {
		if txid != "" && existing.TxID != "" && existing.TxID != txid {
			return Payment{}, ErrConflict
		}
		return existing, nil
	}
	if existing.State != StateSending {
		return Payment{}, ErrConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Payment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	inputs, err := reservedOutpoints(tx, id)
	if err != nil {
		return Payment{}, err
	}
	if len(inputs) > 0 {
		var sum int64
		for _, op := range inputs {
			var sats int64
			if err := tx.QueryRow(`SELECT satoshis FROM utxo WHERE outpoint = ?`, op).Scan(&sats); err != nil {
				return Payment{}, err
			}
			sum += sats
			if _, err := tx.Exec(`DELETE FROM utxo WHERE outpoint = ? AND reserved_by = ?`, op, id); err != nil {
				return Payment{}, err
			}
		}
		change := sum - existing.Amount - existing.Fee
		if change < 0 {
			return Payment{}, ErrInsufficientFunds
		}
		if change > 0 {
			changeID := "sim:change:" + id
			if _, err := tx.Exec(`INSERT INTO utxo(outpoint, satoshis, reserved_by) VALUES(?,?, '')`, changeID, change); err != nil {
				return Payment{}, err
			}
		}
		if _, err := tx.Exec(`DELETE FROM reservation WHERE payment_id = ?`, id); err != nil {
			return Payment{}, err
		}
	}
	whenUnix := when.UTC().Unix()
	if _, err := tx.Exec(`UPDATE payment SET state = ?, txid = ?, output_index = ?, accepted_unix = ?, error = '' WHERE id = ?`,
		StateAccepted, txid, outputIndex, whenUnix, id); err != nil {
		return Payment{}, err
	}
	day := when.UTC().Format("2006-01-02")
	if _, err := tx.Exec(`INSERT INTO daily(day, spend, fees, held_spend, held_fees) VALUES(?,?,?,0,0)
		ON CONFLICT(day) DO UPDATE SET spend = spend + excluded.spend, fees = fees + excluded.fees`,
		day, existing.Amount, existing.Fee); err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Payment{}, err
	}
	p, _, err := s.get(id)
	return p, err
}

// Abort releases a dry-run reservation or records a live failure that is known
// not to have been broadcast. It is not used for an ambiguous wallet error.
func (s *Store) Abort(id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortLocked(id, reason)
}

func (s *Store) abortLocked(id, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	err = tx.QueryRow(`SELECT state FROM payment WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state == StateAccepted || state == StateSeen || state == StateConfirmed {
		return ErrConflict
	}
	if state == StateAborted || state == StateFailed {
		return nil
	}
	if _, err := tx.Exec(`UPDATE utxo SET reserved_by = '' WHERE reserved_by = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM reservation WHERE payment_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE payment SET state = ?, error = ? WHERE id = ?`, StateAborted, reason, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Fail records a definite failure. Sending, accepted, and seen rows can be
// failed (Arcade REJECTED frees the in-flight slot). The payment is looked
// up inside the same transaction because this store uses a single SQLite
// connection; a second query while the tx is open would deadlock.
func (s *Store) Fail(id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	err = tx.QueryRow(`SELECT state FROM payment WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state == StateFailed {
		return nil
	}
	if state != StateSending && state != StateAccepted && state != StateSeen {
		return ErrConflict
	}
	if _, err := tx.Exec(`UPDATE utxo SET reserved_by = '' WHERE reserved_by = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM reservation WHERE payment_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE payment SET state = ?, error = ? WHERE id = ?`, StateFailed, reason, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ConfirmDue marks dry-run payments confirmed once they have been accepted
// long enough. Attempted or merely accepted rows are not counted.
func (s *Store) ConfirmDue(now time.Time, after time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-after).UTC().Unix()
	whenUnix := now.UTC().Unix()
	res, err := s.db.Exec(`UPDATE payment SET state = ?, confirmed_unix = ?, seen_unix = CASE WHEN seen_unix = 0 THEN ? ELSE seen_unix END
		WHERE state IN (?, ?) AND accepted_unix > 0 AND accepted_unix <= ?`,
		StateConfirmed, whenUnix, whenUnix, StateAccepted, StateSeen, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ConfirmID confirms one accepted or seen payment once a merkle path is
// known. It does not confirm a sending or failed row. A payment that jumps
// from accepted to confirmed (MINED skipped the seen event) also records
// seen_unix so the cumulative seen count stays honest.
func (s *Store) ConfirmID(id string, when time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	whenUnix := when.UTC().Unix()
	res, err := s.db.Exec(`UPDATE payment SET state = ?, confirmed_unix = ?, seen_unix = CASE WHEN seen_unix = 0 THEN ? ELSE seen_unix END
		WHERE id = ? AND state IN (?, ?)`,
		StateConfirmed, whenUnix, whenUnix, id, StateAccepted, StateSeen)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// MarkSeen records that Arcade has reported the payment as present on the
// network (SEEN_ON_NETWORK or a later lifecycle status). That frees the
// MaxInFlight slot. Confirmation still requires a merkle path.
// Already-seen or confirmed rows are a no-op success.
func (s *Store) MarkSeen(id string, when time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE payment SET state = ?, seen_unix = ? WHERE id = ? AND state = ?`,
		StateSeen, when.UTC().Unix(), id, StateAccepted)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, nil
	}
	existing, ok, err := s.get(id)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrNotFound
	}
	if existing.State == StateSeen || existing.State == StateConfirmed {
		return false, nil
	}
	return false, ErrConflict
}

// LookupTxID finds a payment by its broadcast txid.
func (s *Store) LookupTxID(txid string) (Payment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if txid == "" {
		return Payment{}, false, nil
	}
	var id string
	err := s.db.QueryRow(`SELECT id FROM payment WHERE txid = ? ORDER BY accepted_unix ASC LIMIT 1`, txid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, err
	}
	return s.get(id)
}

// MarkSeenByTxID applies MarkSeen to the payment that broadcast txid.
func (s *Store) MarkSeenByTxID(txid string, when time.Time) (string, bool, error) {
	p, ok, err := s.LookupTxID(txid)
	if err != nil || !ok {
		return "", false, err
	}
	changed, err := s.MarkSeen(p.ID, when)
	return p.ID, changed, err
}

// Balance is the sum of free dry-run outputs.
func (s *Store) Balance() (free int64, reserved int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT satoshis, reserved_by FROM utxo`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var sats int64
		var by string
		if err := rows.Scan(&sats, &by); err != nil {
			return 0, 0, err
		}
		if by == "" {
			free += sats
		} else {
			reserved += sats
		}
	}
	return free, reserved, rows.Err()
}

// UTXOCount returns free and total dry-run outputs.
func (s *Store) UTXOCount() (free, total int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN reserved_by = '' THEN 1 ELSE 0 END),0),
		COUNT(*) FROM utxo`).Scan(&free, &total)
	return free, total, err
}

// ListUTXOs returns the dry-run set.
func (s *Store) ListUTXOs() ([]ledger.UTXO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadUTXOs(s.db)
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

func loadUTXOs(q queryer) ([]ledger.UTXO, error) {
	rows, err := q.Query(`SELECT outpoint, satoshis, reserved_by FROM utxo ORDER BY outpoint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.UTXO
	for rows.Next() {
		var u ledger.UTXO
		if err := rows.Scan(&u.Outpoint, &u.Satoshis, &u.ReservedBy); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func reservedOutpoints(q queryer, id string) ([]string, error) {
	rows, err := q.Query(`SELECT outpoint FROM reservation WHERE payment_id = ? ORDER BY outpoint`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op string
		if err := rows.Scan(&op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// CreditFunding adds a dry-run output for an internalized Message Box payment.
// The same txid:vout is credited once.
func (s *Store) CreditFunding(f Funding) (created bool, err error) {
	return s.SaveFunding(f, true)
}

// SaveFunding records a message. credit inserts a dry-run coin. A repeated
// outpoint does not create a second coin. Live mode passes credit=false
// because the wallet, not this table, holds the satoshis.
func (s *Store) SaveFunding(f Funding, credit bool) (created bool, err error) {
	if f.MessageID == "" || f.TxID == "" || f.Satoshis <= 0 {
		return false, errors.New("funding record is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingState string
	err = tx.QueryRow(`SELECT state FROM funding WHERE txid = ? AND vout = ? AND message_id != ?`, f.TxID, f.Vout, f.MessageID).Scan(&existingState)
	if err == nil && existingState != FundFailed {
		return false, tx.Commit()
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var existingCoin int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM utxo WHERE outpoint = ?`, fmt.Sprintf("%s:%d", f.TxID, f.Vout)).Scan(&existingCoin); err != nil {
		return false, err
	}
	state := f.State
	if state == "" {
		state = FundInternalized
	}
	if _, err := tx.Exec(`INSERT INTO funding(message_id, sender, txid, vout, satoshis, state, error) VALUES(?,?,?,?,?,?, '')
		ON CONFLICT(message_id) DO UPDATE SET sender=excluded.sender, txid=excluded.txid, vout=excluded.vout,
		satoshis=excluded.satoshis, state=excluded.state, error=''`,
		f.MessageID, f.Sender, f.TxID, f.Vout, f.Satoshis, state); err != nil {
		return false, err
	}
	created = existingCoin == 0
	if credit && existingCoin == 0 {
		outpoint := fmt.Sprintf("%s:%d", f.TxID, f.Vout)
		if _, err := tx.Exec(`INSERT INTO utxo(outpoint, satoshis, reserved_by) VALUES(?,?, '')`, outpoint, f.Satoshis); err != nil {
			return false, err
		}
	}
	return created, tx.Commit()
}

// FundingState returns the stored sweep state.
func (s *Store) FundingState(messageID string) (Funding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f Funding
	err := s.db.QueryRow(`SELECT message_id, sender, txid, vout, satoshis, state, error FROM funding WHERE message_id = ?`, messageID).
		Scan(&f.MessageID, &f.Sender, &f.TxID, &f.Vout, &f.Satoshis, &f.State, &f.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Funding{}, false, nil
	}
	if err != nil {
		return Funding{}, false, err
	}
	return f, true, nil
}

// MarkFundingAck records that the Message Box acknowledge succeeded.
func (s *Store) MarkFundingAck(messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE funding SET state = ?, error = '' WHERE message_id = ?`, FundAcknowledged, messageID)
	return err
}

// MarkFundingError records a sweep failure without acknowledging the message.
func (s *Store) MarkFundingError(messageID, sender, txid string, vout int, sats int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO funding(message_id, sender, txid, vout, satoshis, state, error) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(message_id) DO UPDATE SET state = ?, error = excluded.error`,
		messageID, sender, txid, vout, sats, FundFailed, reason, FundFailed)
	return err
}

// NoteFundingAckPending marks an internalized payment whose acknowledge failed.
func (s *Store) NoteFundingAckPending(messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE funding SET state = ? WHERE message_id = ? AND state != ?`,
		FundAckPending, messageID, FundAcknowledged)
	return err
}

// Split replaces one free output with several. It is a synthetic self-transfer
// in the dry-run ledger: one fee, no payment row.
func (s *Store) Split(parts int, fee int64, day string, feeCap int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	utxos, err := loadUTXOs(s.db)
	if err != nil {
		return false, err
	}
	var src *ledger.UTXO
	for i := range utxos {
		u := utxos[i]
		if u.ReservedBy != "" {
			continue
		}
		if src == nil || u.Satoshis > src.Satoshis {
			src = &utxos[i]
		}
	}
	if src == nil {
		return false, nil
	}
	values, err := ledger.SplitPlan(*src, parts, fee)
	if err != nil {
		return false, nil
	}
	if feeCap > 0 {
		_, fees, _, held, derr := s.daily(day)
		if derr != nil {
			return false, derr
		}
		if fees+held+fee > feeCap {
			return false, nil
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM utxo WHERE outpoint = ? AND reserved_by = ''`, src.Outpoint)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	for i, v := range values {
		op := fmt.Sprintf("sim:split:%s:%d", src.Outpoint, i)
		if _, err := tx.Exec(`INSERT INTO utxo(outpoint, satoshis, reserved_by) VALUES(?,?, '')`, op, v); err != nil {
			return false, err
		}
	}
	if err := addFee(tx, day, fee); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Consolidate merges smallest free outputs down toward target.
func (s *Store) Consolidate(target int, fee int64, day string, feeCap int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	utxos, err := loadUTXOs(s.db)
	if err != nil {
		return false, err
	}
	spent, created, err := ledger.MergePlan(utxos, target, fee)
	if err != nil {
		return false, nil
	}
	if feeCap > 0 {
		_, fees, _, held, derr := s.daily(day)
		if derr != nil {
			return false, derr
		}
		if fees+held+fee > feeCap {
			return false, nil
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, u := range spent {
		res, err := tx.Exec(`DELETE FROM utxo WHERE outpoint = ? AND reserved_by = ''`, u.Outpoint)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return false, nil
		}
	}
	op := fmt.Sprintf("sim:merge:%d", time.Now().UnixNano())
	if _, err := tx.Exec(`INSERT INTO utxo(outpoint, satoshis, reserved_by) VALUES(?,?, '')`, op, created); err != nil {
		return false, err
	}
	if err := addFee(tx, day, fee); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func addFee(tx *sql.Tx, day string, fee int64) error {
	if fee <= 0 {
		return nil
	}
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	_, err := tx.Exec(`INSERT INTO daily(day, spend, fees, held_spend, held_fees) VALUES(?,0,?,0,0)
		ON CONFLICT(day) DO UPDATE SET fees = fees + excluded.fees`, day, fee)
	return err
}

// OutpointFunded reports whether a Message Box outpoint was already credited.
func (s *Store) OutpointFunded(txid string, vout int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM funding WHERE txid = ? AND vout = ? AND state != ?`, txid, vout, FundFailed).Scan(&n)
	return n > 0, err
}

// Counts summarizes payment states. Accepted includes seen and confirmed.
// Seen is cumulative (seen + confirmed). InFlight is accepted-but-not-yet
// seen, plus sending and in-doubt; seen payments no longer occupy a slot.
type Counts struct {
	Attempted int64
	Accepted  int64
	Seen      int64
	Confirmed int64
	Failed    int64
	InDoubt   int64
	InFlight  int64
	Fees      int64
	Spend     int64
	FundAck   int64
	FundPend  int64
	FundFail  int64
}

// Counts reads aggregate counters. Attempted includes every row that reached
// the wallet or the dry-run reservation, including ones still in flight.
func (s *Store) Counts() (Counts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Counts
	rows, err := s.db.Query(`SELECT state, COUNT(*), COALESCE(SUM(fee),0), COALESCE(SUM(amount),0) FROM payment GROUP BY state`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n, fees, amount int64
		if err := rows.Scan(&state, &n, &fees, &amount); err != nil {
			return c, err
		}
		switch state {
		case StateAccepted:
			c.Accepted += n
			c.Attempted += n
			c.InFlight += n
			c.Fees += fees
			c.Spend += amount
		case StateSeen:
			c.Seen += n
			c.Accepted += n
			c.Attempted += n
			c.Fees += fees
			c.Spend += amount
		case StateConfirmed:
			c.Confirmed += n
			c.Seen += n
			c.Accepted += n
			c.Attempted += n
			c.Fees += fees
			c.Spend += amount
		case StateFailed:
			c.Failed += n
			c.Attempted += n
		case StateSending:
			c.Attempted += n
			c.InFlight += n
		case StateInDoubt:
			c.InDoubt += n
			c.Attempted += n
			c.InFlight += n
		}
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	frows, err := s.db.Query(`SELECT state, COUNT(*) FROM funding GROUP BY state`)
	if err != nil {
		return c, err
	}
	defer frows.Close()
	for frows.Next() {
		var state string
		var n int64
		if err := frows.Scan(&state, &n); err != nil {
			return c, err
		}
		switch state {
		case FundAcknowledged:
			c.FundAck += n
		case FundInternalized, FundAckPending:
			c.FundPend += n
		case FundFailed:
			c.FundFail += n
		}
	}
	return c, frows.Err()
}

// AcceptedIDs lists payments still waiting for a mined merkle-path
// confirmation (accepted or seen), oldest first.
func (s *Store) AcceptedIDs(limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id FROM payment WHERE state IN (?, ?) ORDER BY accepted_unix ASC LIMIT ?`, StateAccepted, StateSeen, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UnconfirmedLive lists accepted or seen broadcast payments that have a real
// txid and are still waiting for a mined merkle path in this store. The
// confirm poll uses this full set so it is not stuck on the oldest 50
// still-unproven wallet actions while later payments are already completed.
func (s *Store) UnconfirmedLive() ([]Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, txid FROM payment
		WHERE state IN (?, ?) AND txid != '' AND txid NOT LIKE 'sim:%'
		ORDER BY accepted_unix ASC`, StateAccepted, StateSeen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.TxID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UnseenBroadcast lists accepted (not yet seen) live payments that have a
// real txid, for Arcade status fallback polling.
func (s *Store) UnseenBroadcast(limit int) ([]Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, txid FROM payment
		WHERE state = ? AND txid != '' AND txid NOT LIKE 'sim:%'
		ORDER BY accepted_unix ASC LIMIT ?`, StateAccepted, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.TxID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AcceptedKinds splits accepted, seen, and confirmed payments into dry-run
// ids and broadcast ids. A dry-run id starts with "sim:" or is still empty.
func (s *Store) AcceptedKinds() (dryRun, broadcast int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN txid = '' OR txid LIKE 'sim:%' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN txid != '' AND txid NOT LIKE 'sim:%' THEN 1 ELSE 0 END), 0)
		FROM payment WHERE state IN (?, ?, ?)`, StateAccepted, StateSeen, StateConfirmed).Scan(&dryRun, &broadcast)
	return dryRun, broadcast, err
}

// RecentPayment is a public row for the health console. It has no keys.
type RecentPayment struct {
	ID      string
	TxID    string
	Amount  int64
	Fee     int64
	State   string
	Created time.Time
	DryRun  bool
}

// RecentPayments returns the newest payment rows.
func (s *Store) RecentPayments(limit int) ([]RecentPayment, error) {
	if limit < 1 || limit > 50 {
		limit = 12
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, txid, amount, fee, state, created_unix FROM payment
		ORDER BY created_unix DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentPayment
	for rows.Next() {
		var p RecentPayment
		var created int64
		if err := rows.Scan(&p.ID, &p.TxID, &p.Amount, &p.Fee, &p.State, &created); err != nil {
			return nil, err
		}
		p.Created = time.Unix(created, 0).UTC()
		p.DryRun = p.TxID == "" || len(p.TxID) < 4 || p.TxID[:4] == "sim:"
		out = append(out, p)
	}
	return out, rows.Err()
}

// Ping lets health checks see a closed database.
func (s *Store) Ping(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.PingContext(ctx)
}
