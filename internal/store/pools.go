package store

import (
	"database/sql"
	"errors"

	"github.com/biswas-dev/pool/internal/chem"
)

const poolCols = `id, user_id, name, customer_name, site_address, volume_l, water_type, treatment_profile, grade, surface, sanitizer, location, salt_pool, created_at`

func scanPool(row interface{ Scan(...any) error }) (*Pool, error) {
	var p Pool
	var salt int64
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.CustomerName, &p.SiteAddress, &p.VolumeL, &p.WaterType,
		&p.TreatmentProfile, &p.Grade, &p.Surface, &p.Sanitizer, &p.Location, &salt, &p.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.SaltPool = salt != 0
	return &p, nil
}

// Profile converts a stored pool into the chemistry engine's view of it.
func (p *Pool) Profile() chem.Profile {
	return chem.Profile{
		VolumeL:   p.VolumeL,
		Sanitizer: chem.ParseSanitizer(p.Sanitizer),
		Surface:   chem.ParseSurface(p.Surface),
		SaltPool:  p.SaltPool,
	}
}

func (db *DB) CreatePool(p *Pool) (*Pool, error) {
	res, err := db.Exec(`INSERT INTO pools(user_id, name, customer_name, site_address, volume_l, water_type, treatment_profile, grade, surface, sanitizer, location, salt_pool, created_at)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.UserID, p.Name, p.CustomerName, p.SiteAddress, p.VolumeL, p.WaterType, p.TreatmentProfile,
		p.Grade, p.Surface, p.Sanitizer, p.Location, boolInt(p.SaltPool), Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.Pool(p.UserID, id)
}

func (db *DB) UpdatePool(p *Pool) error {
	res, err := db.Exec(`UPDATE pools SET name=?, customer_name=?, site_address=?, volume_l=?, water_type=?, treatment_profile=?, grade=?, surface=?, sanitizer=?, location=?, salt_pool=?
	  WHERE id=? AND user_id=?`,
		p.Name, p.CustomerName, p.SiteAddress, p.VolumeL, p.WaterType, p.TreatmentProfile, p.Grade,
		p.Surface, p.Sanitizer, p.Location, boolInt(p.SaltPool), p.ID, p.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Pool fetches one pool, scoped to its owner.
func (db *DB) Pool(userID, id int64) (*Pool, error) {
	return scanPool(db.QueryRow(`SELECT `+poolCols+` FROM pools WHERE id = ? AND user_id = ?`, id, userID))
}

func (db *DB) ListPools(userID int64) ([]Pool, error) {
	rows, err := db.Query(`SELECT `+poolCols+` FROM pools WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// DeletePool removes a pool and everything hanging off it. The engine does not
// enforce ON DELETE CASCADE, so children are removed here, deepest first.
// Attachment files on disk are the caller's to clean up; StoredNamesForPool
// returns them.
func (db *DB) DeletePool(userID, id int64) error {
	if _, err := db.Pool(userID, id); err != nil {
		return err
	}
	// The engine does not support IN (subquery) either, so the child ids are
	// resolved first and deleted by key.
	for _, link := range []struct{ idQuery, deleteQuery string }{
		{`SELECT id FROM receipts WHERE pool_id = ?`, `DELETE FROM receipt_links WHERE receipt_id = ?`},
		{`SELECT id FROM log_entries WHERE pool_id = ?`, `DELETE FROM receipt_links WHERE log_entry_id = ?`},
		{`SELECT id FROM tests WHERE pool_id = ?`, `DELETE FROM treatments WHERE test_id = ?`},
	} {
		ids, err := db.ids(link.idQuery, id)
		if err != nil {
			return err
		}
		for _, childID := range ids {
			if _, err := db.Exec(link.deleteQuery, childID); err != nil {
				return err
			}
		}
	}

	for _, q := range []string{
		`DELETE FROM receipts WHERE pool_id = ?`,
		`DELETE FROM log_entries WHERE pool_id = ?`,
		`DELETE FROM notes WHERE pool_id = ?`,
		`DELETE FROM tests WHERE pool_id = ?`,
		`DELETE FROM seasons WHERE pool_id = ?`,
	} {
		if _, err := db.Exec(q, id); err != nil {
			return err
		}
	}
	res, err := db.Exec(`DELETE FROM pools WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StoredNamesForPool lists the on-disk filenames of a pool's attachments, so
// they can be removed before the pool is deleted.
func (db *DB) StoredNamesForPool(poolID int64) ([]string, error) {
	rows, err := db.Query(`SELECT stored_name FROM receipts WHERE pool_id = ?`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Companies
// ---------------------------------------------------------------------------

const companyCols = `id, user_id, name, kind, phone, email, address, notes, created_at`

func scanCompany(row interface{ Scan(...any) error }) (*Company, error) {
	var c Company
	if err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Kind, &c.Phone, &c.Email, &c.Address, &c.Notes, &c.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (db *DB) CreateCompany(c *Company) (*Company, error) {
	res, err := db.Exec(`INSERT INTO companies(user_id, name, kind, phone, email, address, notes, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		c.UserID, c.Name, c.Kind, c.Phone, c.Email, c.Address, c.Notes, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanCompany(db.QueryRow(`SELECT `+companyCols+` FROM companies WHERE id = ?`, id))
}

func (db *DB) UpdateCompany(c *Company) error {
	res, err := db.Exec(`UPDATE companies SET name=?, kind=?, phone=?, email=?, address=?, notes=? WHERE id=? AND user_id=?`,
		c.Name, c.Kind, c.Phone, c.Email, c.Address, c.Notes, c.ID, c.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) Company(userID, id int64) (*Company, error) {
	return scanCompany(db.QueryRow(`SELECT `+companyCols+` FROM companies WHERE id = ? AND user_id = ?`, id, userID))
}

// CompanyByName finds a company by name, creating it if it does not exist.
// This lets the API accept a store name without a prior lookup.
func (db *DB) CompanyByName(userID int64, name, kind string) (*Company, error) {
	c, err := scanCompany(db.QueryRow(`SELECT `+companyCols+` FROM companies WHERE user_id = ? AND name = ?`, userID, name))
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return db.CreateCompany(&Company{UserID: userID, Name: name, Kind: kind})
}

func (db *DB) ListCompanies(userID int64) ([]Company, error) {
	rows, err := db.Query(`SELECT `+companyCols+` FROM companies WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Company{}
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (db *DB) DeleteCompany(userID, id int64) error {
	res, err := db.Exec(`DELETE FROM companies WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
