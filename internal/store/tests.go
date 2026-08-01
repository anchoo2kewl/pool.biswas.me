package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// testCols must stay in sync with scanTest.
const testCols = `t.id, t.pool_id, t.company_id, t.tested_at, t.operator, t.test_count, t.source,
 t.free_chlorine, t.total_chlorine, t.combined_chlorine, t.total_salt, t.bromine,
 t.ph, t.total_alkalinity, t.total_alkalinity_adjusted, t.calcium_hardness,
 t.cyanuric_acid, t.phosphate, t.borate, t.tds, t.temperature,
 t.total_copper, t.free_copper, t.combined_copper, t.iron,
 t.wqi, t.lsi, t.score, t.weather, t.created_at, COALESCE(c.name,'')`

func scanTest(row interface{ Scan(...any) error }) (*Test, error) {
	var t Test
	err := row.Scan(&t.ID, &t.PoolID, &t.CompanyID, &t.TestedAt, &t.Operator, &t.TestCount, &t.Source,
		&t.FreeChlorine, &t.TotalChlorine, &t.CombinedChlorine, &t.TotalSalt, &t.Bromine,
		&t.PH, &t.TotalAlkalinity, &t.TotalAlkalinityAdjusted, &t.CalciumHardness,
		&t.CyanuricAcid, &t.Phosphate, &t.Borate, &t.TDS, &t.Temperature,
		&t.TotalCopper, &t.FreeCopper, &t.CombinedCopper, &t.Iron,
		&t.WQI, &t.LSI, &t.Score, &t.Weather, &t.CreatedAt, &t.CompanyName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

const testFrom = ` FROM tests t LEFT JOIN companies c ON c.id = t.company_id`

func (db *DB) CreateTest(t *Test) (*Test, error) {
	res, err := db.Exec(`INSERT INTO tests(pool_id, company_id, tested_at, operator, test_count, source,
	  free_chlorine, total_chlorine, combined_chlorine, total_salt, bromine,
	  ph, total_alkalinity, total_alkalinity_adjusted, calcium_hardness,
	  cyanuric_acid, phosphate, borate, tds, temperature,
	  total_copper, free_copper, combined_copper, iron,
	  wqi, lsi, score, weather, created_at)
	  VALUES(?,?,?,?,?,?, ?,?,?,?,?, ?,?,?,?, ?,?,?,?,?, ?,?,?,?, ?,?,?,?,?)`,
		t.PoolID, t.CompanyID, t.TestedAt, t.Operator, t.TestCount, t.Source,
		t.FreeChlorine, t.TotalChlorine, t.CombinedChlorine, t.TotalSalt, t.Bromine,
		t.PH, t.TotalAlkalinity, t.TotalAlkalinityAdjusted, t.CalciumHardness,
		t.CyanuricAcid, t.Phosphate, t.Borate, t.TDS, t.Temperature,
		t.TotalCopper, t.FreeCopper, t.CombinedCopper, t.Iron,
		t.WQI, t.LSI, t.Score, t.Weather, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.TestByID(id)
}

func (db *DB) UpdateTest(t *Test) error {
	res, err := db.Exec(`UPDATE tests SET company_id=?, tested_at=?, operator=?, test_count=?, source=?,
	  free_chlorine=?, total_chlorine=?, combined_chlorine=?, total_salt=?, bromine=?,
	  ph=?, total_alkalinity=?, total_alkalinity_adjusted=?, calcium_hardness=?,
	  cyanuric_acid=?, phosphate=?, borate=?, tds=?, temperature=?,
	  total_copper=?, free_copper=?, combined_copper=?, iron=?,
	  wqi=?, lsi=?, score=?, weather=? WHERE id=?`,
		t.CompanyID, t.TestedAt, t.Operator, t.TestCount, t.Source,
		t.FreeChlorine, t.TotalChlorine, t.CombinedChlorine, t.TotalSalt, t.Bromine,
		t.PH, t.TotalAlkalinity, t.TotalAlkalinityAdjusted, t.CalciumHardness,
		t.CyanuricAcid, t.Phosphate, t.Borate, t.TDS, t.Temperature,
		t.TotalCopper, t.FreeCopper, t.CombinedCopper, t.Iron,
		t.WQI, t.LSI, t.Score, t.Weather, t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) TestByID(id int64) (*Test, error) {
	return scanTest(db.QueryRow(`SELECT `+testCols+testFrom+` WHERE t.id = ?`, id))
}

// Test fetches a test and verifies it belongs to the user, via its pool.
func (db *DB) Test(userID, id int64) (*Test, error) {
	t, err := scanTest(db.QueryRow(`SELECT `+testCols+testFrom+
		` JOIN pools p ON p.id = t.pool_id WHERE t.id = ? AND p.user_id = ?`, id, userID))
	return t, err
}

// TestFilter narrows a test listing.
type TestFilter struct {
	PoolID int64
	From   string // inclusive date/timestamp
	To     string // inclusive
	Limit  int
}

func (db *DB) ListTests(userID int64, f TestFilter) ([]Test, error) {
	q := `SELECT ` + testCols + testFrom + ` JOIN pools p ON p.id = t.pool_id WHERE p.user_id = ?`
	args := []any{userID}
	if f.PoolID > 0 {
		q += ` AND t.pool_id = ?`
		args = append(args, f.PoolID)
	}
	if f.From != "" {
		q += ` AND t.tested_at >= ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND t.tested_at <= ?`
		args = append(args, f.To+"T23:59:59Z")
	}
	q += ` ORDER BY t.tested_at DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Test{}
	for rows.Next() {
		t, err := scanTest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// DeleteTest removes a test with its treatments and notes. Log entries and
// attachments survive — the money was still spent, and the receipt is still a
// record — but they lose their reference to the test.
func (db *DB) DeleteTest(userID, id int64) error {
	// Confirm ownership first; the delete itself cannot join.
	if _, err := db.Test(userID, id); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM treatments WHERE test_id = ?`,
		`DELETE FROM notes WHERE test_id = ?`,
		`UPDATE log_entries SET test_id = NULL WHERE test_id = ?`,
		`UPDATE receipts SET test_id = NULL WHERE test_id = ?`,
		`DELETE FROM tests WHERE id = ?`,
	} {
		if _, err := db.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// LatestTest returns the most recent test for a pool, or ErrNotFound.
func (db *DB) LatestTest(poolID int64) (*Test, error) {
	return scanTest(db.QueryRow(`SELECT `+testCols+testFrom+` WHERE t.pool_id = ? ORDER BY t.tested_at DESC LIMIT 1`, poolID))
}

// ---------------------------------------------------------------------------
// Notes
// ---------------------------------------------------------------------------

const noteCols = `id, test_id, pool_id, user_id, kind, body, model, meta, created_at`

func scanNote(row interface{ Scan(...any) error }) (*Note, error) {
	var n Note
	if err := row.Scan(&n.ID, &n.TestID, &n.PoolID, &n.UserID, &n.Kind, &n.Body, &n.Model, &n.Meta, &n.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

func (db *DB) CreateNote(n *Note) (*Note, error) {
	res, err := db.Exec(`INSERT INTO notes(test_id, pool_id, user_id, kind, body, model, meta, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		n.TestID, n.PoolID, n.UserID, n.Kind, n.Body, n.Model, n.Meta, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanNote(db.QueryRow(`SELECT `+noteCols+` FROM notes WHERE id = ?`, id))
}

func (db *DB) ListNotes(poolID int64, testID *int64) ([]Note, error) {
	q := `SELECT ` + noteCols + ` FROM notes WHERE pool_id = ?`
	args := []any{poolID}
	if testID != nil {
		q += ` AND test_id = ?`
		args = append(args, *testID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func (db *DB) UpdateNote(poolID, id int64, body string) error {
	res, err := db.Exec(`UPDATE notes SET body = ? WHERE id = ? AND pool_id = ?`, body, id, poolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) DeleteNote(poolID, id int64) error {
	res, err := db.Exec(`DELETE FROM notes WHERE id = ? AND pool_id = ?`, id, poolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Treatments
// ---------------------------------------------------------------------------

const treatmentCols = `id, test_id, parameter, product, amount, unit, reason, note, priority, source, applied, COALESCE(applied_at,''), created_at`

func scanTreatment(row interface{ Scan(...any) error }) (*Treatment, error) {
	var t Treatment
	var applied int64
	if err := row.Scan(&t.ID, &t.TestID, &t.Parameter, &t.Product, &t.Amount, &t.Unit, &t.Reason, &t.Note,
		&t.Priority, &t.Source, &applied, &t.AppliedAt, &t.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Applied = applied != 0
	return &t, nil
}

func (db *DB) CreateTreatment(t *Treatment) (*Treatment, error) {
	res, err := db.Exec(`INSERT INTO treatments(test_id, parameter, product, amount, unit, reason, note, priority, source, applied, created_at)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		t.TestID, t.Parameter, t.Product, t.Amount, t.Unit, t.Reason, t.Note, t.Priority, t.Source, boolInt(t.Applied), Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanTreatment(db.QueryRow(`SELECT `+treatmentCols+` FROM treatments WHERE id = ?`, id))
}

// ReplaceComputedTreatments swaps the machine-generated recommendations for a
// test, leaving anything a human entered untouched.
func (db *DB) ReplaceComputedTreatments(testID int64, ts []Treatment) error {
	if _, err := db.Exec(`DELETE FROM treatments WHERE test_id = ? AND source = 'computed' AND applied = 0`, testID); err != nil {
		return err
	}
	for i := range ts {
		ts[i].TestID = testID
		ts[i].Source = "computed"
		if _, err := db.CreateTreatment(&ts[i]); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) ListTreatments(testID int64) ([]Treatment, error) {
	rows, err := db.Query(`SELECT `+treatmentCols+` FROM treatments WHERE test_id = ? ORDER BY priority, id`, testID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Treatment{}
	for rows.Next() {
		t, err := scanTreatment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// MarkTreatmentApplied records that a recommendation was actually carried out.
func (db *DB) MarkTreatmentApplied(id int64, applied bool) error {
	var appliedAt any
	if applied {
		appliedAt = Now()
	}
	res, err := db.Exec(`UPDATE treatments SET applied = ?, applied_at = ? WHERE id = ?`, boolInt(applied), appliedAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TreatmentOwner returns the pool a treatment belongs to, for authorisation.
func (db *DB) TreatmentOwner(userID, treatmentID int64) (int64, error) {
	var poolID int64
	err := db.QueryRow(`SELECT t.pool_id FROM treatments tr JOIN tests t ON t.id = tr.test_id JOIN pools p ON p.id = t.pool_id
	  WHERE tr.id = ? AND p.user_id = ?`, treatmentID, userID).Scan(&poolID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return poolID, err
}

// NullIfEmpty converts an empty string to a NULL-able value for optional cols.
func NullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
