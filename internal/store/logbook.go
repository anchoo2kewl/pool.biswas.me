package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// OAuth identities
// ---------------------------------------------------------------------------

// LinkIdentity records an OAuth identity against a user, updating the stored
// profile if the identity already exists.
func (db *DB) LinkIdentity(provider, uid string, userID int64, email, avatar string) error {
	_, err := db.Exec(`INSERT INTO oauth_identities(provider, provider_uid, user_id, email, avatar_url, created_at)
	  VALUES(?,?,?,?,?,?)
	  ON CONFLICT(provider, provider_uid) DO UPDATE SET email = excluded.email, avatar_url = excluded.avatar_url`,
		provider, uid, userID, email, avatar, Now())
	return err
}

// UserByIdentity resolves an OAuth identity to an account.
func (db *DB) UserByIdentity(provider, uid string) (*User, error) {
	var userID int64
	err := db.QueryRow(`SELECT user_id FROM oauth_identities WHERE provider = ? AND provider_uid = ?`, provider, uid).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return db.UserByID(userID)
}

// ListIdentities returns the providers linked to an account.
func (db *DB) ListIdentities(userID int64) ([]string, error) {
	rows, err := db.Query(`SELECT provider FROM oauth_identities WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Seasons
// ---------------------------------------------------------------------------

const seasonCols = `id, pool_id, name, opened_on, COALESCE(closed_on,''), notes, created_at`

func scanSeason(row interface{ Scan(...any) error }) (*Season, error) {
	var s Season
	if err := row.Scan(&s.ID, &s.PoolID, &s.Name, &s.OpenedOn, &s.ClosedOn, &s.Notes, &s.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (db *DB) CreateSeason(s *Season) (*Season, error) {
	res, err := db.Exec(`INSERT INTO seasons(pool_id, name, opened_on, closed_on, notes, created_at) VALUES(?,?,?,?,?,?)`,
		s.PoolID, s.Name, s.OpenedOn, NullIfEmpty(s.ClosedOn), s.Notes, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	// Any backdated entries that fall inside the new season adopt it.
	if err := db.reassignSeasonEntries(s.PoolID); err != nil {
		return nil, err
	}
	return scanSeason(db.QueryRow(`SELECT `+seasonCols+` FROM seasons WHERE id = ?`, id))
}

func (db *DB) UpdateSeason(s *Season) error {
	res, err := db.Exec(`UPDATE seasons SET name=?, opened_on=?, closed_on=?, notes=? WHERE id=? AND pool_id=?`,
		s.Name, s.OpenedOn, NullIfEmpty(s.ClosedOn), s.Notes, s.ID, s.PoolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return db.reassignSeasonEntries(s.PoolID)
}

func (db *DB) Season(poolID, id int64) (*Season, error) {
	return scanSeason(db.QueryRow(`SELECT `+seasonCols+` FROM seasons WHERE id = ? AND pool_id = ?`, id, poolID))
}

// ListSeasons returns a pool's seasons, newest first, with costs rolled up.
func (db *DB) ListSeasons(poolID int64) ([]Season, error) {
	rows, err := db.Query(`SELECT `+seasonCols+` FROM seasons WHERE pool_id = ? ORDER BY opened_on DESC`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Season{}
	for rows.Next() {
		s, err := scanSeason(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		row := db.QueryRow(`SELECT COALESCE(SUM(cost_cents),0), COUNT(*) FROM log_entries WHERE season_id = ?`, out[i].ID)
		if err := row.Scan(&out[i].TotalCents, &out[i].EntryCount); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (db *DB) DeleteSeason(poolID, id int64) error {
	res, err := db.Exec(`DELETE FROM seasons WHERE id = ? AND pool_id = ?`, id, poolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeasonForDate finds the season whose window contains a date. An open-ended
// season (no closing date) extends to the present.
func (db *DB) SeasonForDate(poolID int64, date string) (*int64, error) {
	var id int64
	err := db.QueryRow(`SELECT id FROM seasons WHERE pool_id = ? AND opened_on <= ?
	  AND (closed_on IS NULL OR closed_on = '' OR closed_on >= ?) ORDER BY opened_on DESC LIMIT 1`,
		poolID, date, date).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// reassignSeasonEntries re-files every entry for a pool into whichever season
// now contains its date. Called after a season's window changes, so backdated
// entries land in the right place.
func (db *DB) reassignSeasonEntries(poolID int64) error {
	rows, err := db.Query(`SELECT id, occurred_on FROM log_entries WHERE pool_id = ?`, poolID)
	if err != nil {
		return err
	}
	type entry struct {
		id   int64
		date string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.date); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, e := range entries {
		seasonID, err := db.SeasonForDate(poolID, e.date)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE log_entries SET season_id = ? WHERE id = ?`, seasonID, e.id); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Log entries
// ---------------------------------------------------------------------------

const logCols = `l.id, l.pool_id, l.season_id, l.company_id, l.test_id, l.user_id, l.occurred_on, l.category,
 l.item, l.quantity, l.unit, l.cost_cents, l.currency, l.vendor, l.notes, l.created_at, l.updated_at,
 COALESCE(c.name,''), COALESCE(s.name,'')`

const logFrom = ` FROM log_entries l LEFT JOIN companies c ON c.id = l.company_id LEFT JOIN seasons s ON s.id = l.season_id`

func scanLogEntry(row interface{ Scan(...any) error }) (*LogEntry, error) {
	var l LogEntry
	if err := row.Scan(&l.ID, &l.PoolID, &l.SeasonID, &l.CompanyID, &l.TestID, &l.UserID, &l.OccurredOn, &l.Category,
		&l.Item, &l.Quantity, &l.Unit, &l.CostCents, &l.Currency, &l.Vendor, &l.Notes, &l.CreatedAt, &l.UpdatedAt,
		&l.CompanyName, &l.SeasonName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &l, nil
}

// CreateLogEntry files an entry, assigning it to whichever season contains its
// date. Backdating is the normal path.
func (db *DB) CreateLogEntry(l *LogEntry) (*LogEntry, error) {
	if l.SeasonID == nil {
		seasonID, err := db.SeasonForDate(l.PoolID, l.OccurredOn)
		if err != nil {
			return nil, err
		}
		l.SeasonID = seasonID
	}
	if l.Currency == "" {
		l.Currency = "CAD"
	}
	now := Now()
	res, err := db.Exec(`INSERT INTO log_entries(pool_id, season_id, company_id, test_id, user_id, occurred_on,
	  category, item, quantity, unit, cost_cents, currency, vendor, notes, created_at, updated_at)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.PoolID, l.SeasonID, l.CompanyID, l.TestID, l.UserID, l.OccurredOn, l.Category, l.Item,
		l.Quantity, l.Unit, l.CostCents, l.Currency, l.Vendor, l.Notes, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.LogEntryByID(id)
}

func (db *DB) UpdateLogEntry(l *LogEntry) error {
	seasonID, err := db.SeasonForDate(l.PoolID, l.OccurredOn)
	if err != nil {
		return err
	}
	l.SeasonID = seasonID
	res, err := db.Exec(`UPDATE log_entries SET season_id=?, company_id=?, test_id=?, occurred_on=?, category=?,
	  item=?, quantity=?, unit=?, cost_cents=?, currency=?, vendor=?, notes=?, updated_at=?
	  WHERE id=? AND pool_id=?`,
		l.SeasonID, l.CompanyID, l.TestID, l.OccurredOn, l.Category, l.Item, l.Quantity, l.Unit,
		l.CostCents, l.Currency, l.Vendor, l.Notes, Now(), l.ID, l.PoolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) LogEntryByID(id int64) (*LogEntry, error) {
	l, err := scanLogEntry(db.QueryRow(`SELECT `+logCols+logFrom+` WHERE l.id = ?`, id))
	if err != nil {
		return nil, err
	}
	l.ReceiptIDs, err = db.receiptIDsFor(l.ID)
	return l, err
}

// LogFilter narrows a logbook listing.
type LogFilter struct {
	PoolID   int64
	SeasonID int64
	Category string
	From     string
	To       string
	Limit    int
}

func (db *DB) ListLogEntries(userID int64, f LogFilter) ([]LogEntry, error) {
	q := `SELECT ` + logCols + logFrom + ` JOIN pools p ON p.id = l.pool_id WHERE p.user_id = ?`
	args := []any{userID}
	if f.PoolID > 0 {
		q += ` AND l.pool_id = ?`
		args = append(args, f.PoolID)
	}
	if f.SeasonID > 0 {
		q += ` AND l.season_id = ?`
		args = append(args, f.SeasonID)
	}
	if f.Category != "" {
		q += ` AND l.category = ?`
		args = append(args, f.Category)
	}
	if f.From != "" {
		q += ` AND l.occurred_on >= ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND l.occurred_on <= ?`
		args = append(args, f.To)
	}
	q += ` ORDER BY l.occurred_on DESC, l.id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		l, err := scanLogEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ids, err := db.receiptIDsFor(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ReceiptIDs = ids
	}
	return out, nil
}

func (db *DB) DeleteLogEntry(poolID, id int64) error {
	res, err := db.Exec(`DELETE FROM log_entries WHERE id = ? AND pool_id = ?`, id, poolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LogEntryOwner returns the pool an entry belongs to, for authorisation.
func (db *DB) LogEntryOwner(userID, entryID int64) (int64, error) {
	var poolID int64
	err := db.QueryRow(`SELECT l.pool_id FROM log_entries l JOIN pools p ON p.id = l.pool_id WHERE l.id = ? AND p.user_id = ?`,
		entryID, userID).Scan(&poolID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return poolID, err
}

// ---------------------------------------------------------------------------
// Receipts and attachments
// ---------------------------------------------------------------------------

const receiptCols = `id, pool_id, user_id, filename, stored_name, content_type, size_bytes, total_cents,
 currency, vendor, COALESCE(purchased_on,''), notes, created_at, kind, test_id, width, height, original_bytes`

func scanReceipt(row interface{ Scan(...any) error }) (*Receipt, error) {
	var r Receipt
	var kind string
	var testID sql.NullInt64
	var width, height, originalBytes int64
	if err := row.Scan(&r.ID, &r.PoolID, &r.UserID, &r.Filename, &r.StoredName, &r.ContentType, &r.SizeBytes,
		&r.TotalCents, &r.Currency, &r.Vendor, &r.PurchasedOn, &r.Notes, &r.CreatedAt,
		&kind, &testID, &width, &height, &originalBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Kind, r.Width, r.Height, r.OriginalBytes = kind, width, height, originalBytes
	if testID.Valid {
		r.TestID = &testID.Int64
	}
	return &r, nil
}

func (db *DB) CreateReceipt(r *Receipt) (*Receipt, error) {
	if r.Currency == "" {
		r.Currency = "CAD"
	}
	if r.Kind == "" {
		r.Kind = "receipt"
	}
	res, err := db.Exec(`INSERT INTO receipts(pool_id, user_id, filename, stored_name, content_type, size_bytes,
	  total_cents, currency, vendor, purchased_on, notes, created_at, kind, test_id, width, height, original_bytes)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.PoolID, r.UserID, r.Filename, r.StoredName, r.ContentType, r.SizeBytes, r.TotalCents, r.Currency,
		r.Vendor, NullIfEmpty(r.PurchasedOn), r.Notes, Now(), r.Kind, r.TestID, r.Width, r.Height, r.OriginalBytes)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.ReceiptByID(id)
}

func (db *DB) ReceiptByID(id int64) (*Receipt, error) {
	r, err := scanReceipt(db.QueryRow(`SELECT `+receiptCols+` FROM receipts WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	r.LinkedEntryIDs, err = db.entryIDsFor(r.ID)
	return r, err
}

// Receipt fetches an attachment scoped to its owner.
func (db *DB) Receipt(userID, id int64) (*Receipt, error) {
	var poolID int64
	err := db.QueryRow(`SELECT r.pool_id FROM receipts r JOIN pools p ON p.id = r.pool_id WHERE r.id = ? AND p.user_id = ?`,
		id, userID).Scan(&poolID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return db.ReceiptByID(id)
}

// ReceiptFilter narrows an attachment listing.
type ReceiptFilter struct {
	PoolID int64
	Kind   string
	TestID int64
}

func (db *DB) ListReceipts(userID int64, f ReceiptFilter) ([]Receipt, error) {
	q := `SELECT r.id, r.pool_id, r.user_id, r.filename, r.stored_name, r.content_type, r.size_bytes, r.total_cents,
	 r.currency, r.vendor, COALESCE(r.purchased_on,''), r.notes, r.created_at, r.kind, r.test_id, r.width, r.height, r.original_bytes
	 FROM receipts r JOIN pools p ON p.id = r.pool_id WHERE p.user_id = ?`
	args := []any{userID}
	if f.PoolID > 0 {
		q += ` AND r.pool_id = ?`
		args = append(args, f.PoolID)
	}
	if f.Kind != "" {
		q += ` AND r.kind = ?`
		args = append(args, f.Kind)
	}
	if f.TestID > 0 {
		q += ` AND r.test_id = ?`
		args = append(args, f.TestID)
	}
	q += ` ORDER BY r.created_at DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Receipt{}
	for rows.Next() {
		r, err := scanReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ids, err := db.entryIDsFor(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].LinkedEntryIDs = ids
	}
	return out, nil
}

func (db *DB) UpdateReceipt(r *Receipt) error {
	res, err := db.Exec(`UPDATE receipts SET total_cents=?, currency=?, vendor=?, purchased_on=?, notes=?, kind=?, test_id=?
	  WHERE id=? AND pool_id=?`,
		r.TotalCents, r.Currency, r.Vendor, NullIfEmpty(r.PurchasedOn), r.Notes, r.Kind, r.TestID, r.ID, r.PoolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteReceipt removes the row and returns the stored filename so the caller
// can delete the file from disk.
func (db *DB) DeleteReceipt(userID, id int64) (string, error) {
	r, err := db.Receipt(userID, id)
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`DELETE FROM receipts WHERE id = ?`, id); err != nil {
		return "", err
	}
	return r.StoredName, nil
}

// LinkReceipt attaches a receipt to a log entry.
func (db *DB) LinkReceipt(receiptID, entryID int64) error {
	_, err := db.Exec(`INSERT INTO receipt_links(receipt_id, log_entry_id) VALUES(?,?)
	  ON CONFLICT(receipt_id, log_entry_id) DO NOTHING`, receiptID, entryID)
	return err
}

func (db *DB) UnlinkReceipt(receiptID, entryID int64) error {
	_, err := db.Exec(`DELETE FROM receipt_links WHERE receipt_id = ? AND log_entry_id = ?`, receiptID, entryID)
	return err
}

func (db *DB) receiptIDsFor(entryID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT receipt_id FROM receipt_links WHERE log_entry_id = ?`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (db *DB) entryIDsFor(receiptID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT log_entry_id FROM receipt_links WHERE receipt_id = ?`, receiptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
