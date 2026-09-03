package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const userCols = `id, email, name, role, COALESCE(ai_api_key,''), COALESCE(ai_base_url,''), COALESCE(ai_model,''), created_at, COALESCE(last_login_at,''), COALESCE(totp_confirmed_at,'')`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var totpConfirmed string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.AIAPIKey, &u.AIBaseURL, &u.AIModel,
		&u.CreatedAt, &u.LastLoginAt, &totpConfirmed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.HasAIKey = u.AIAPIKey != ""
	u.MFAEnabled = totpConfirmed != ""
	return &u, nil
}

// CreateUser inserts a new account. Email is normalised to lowercase.
func (db *DB) CreateUser(email, name, passwordHash, role string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	res, err := db.Exec(`INSERT INTO users(email, name, password_hash, role, created_at) VALUES(?,?,?,?,?)`,
		email, name, passwordHash, role, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.UserByID(id)
}

func (db *DB) UserByID(id int64) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (db *DB) UserByEmail(email string) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))))
}

// PasswordHash returns the stored hash for a login attempt.
func (db *DB) PasswordHash(email string) (int64, string, error) {
	var id int64
	var hash string
	err := db.QueryRow(`SELECT id, password_hash FROM users WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	return id, hash, err
}

func (db *DB) SetPassword(userID int64, hash string) error {
	_, err := db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

func (db *DB) TouchLogin(userID int64) error {
	_, err := db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, Now(), userID)
	return err
}

// SetAISettings stores the user's own LLM credential and model choice. An
// empty key clears it.
func (db *DB) SetAISettings(userID int64, apiKey, baseURL, model string) error {
	_, err := db.Exec(`UPDATE users SET ai_api_key = ?, ai_base_url = ?, ai_model = ? WHERE id = ?`, apiKey, baseURL, model, userID)
	return err
}

func (db *DB) CountUsers() (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.Query(`SELECT ` + userCols + ` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (db *DB) CreateSession(token string, userID int64, ttl time.Duration, userAgent string) error {
	_, err := db.Exec(`INSERT INTO sessions(token, user_id, created_at, expires_at, user_agent) VALUES(?,?,?,?,?)`,
		token, userID, Now(), time.Now().UTC().Add(ttl).Format(time.RFC3339), userAgent)
	return err
}

// UserBySession resolves a session token, rejecting expired sessions.
func (db *DB) UserBySession(token string) (*User, error) {
	var userID int64
	var expires string
	err := db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().UTC().After(exp) {
		db.DeleteSession(token)
		return nil, ErrNotFound
	}
	return db.UserByID(userID)
}

func (db *DB) DeleteSession(token string) error {
	_, err := db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// PurgeExpiredSessions clears sessions that are past their expiry.
func (db *DB) PurgeExpiredSessions() error {
	_, err := db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, Now())
	return err
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

const apiKeyCols = `id, user_id, name, prefix, scopes, created_at, COALESCE(last_used_at,''), COALESCE(expires_at,''), COALESCE(revoked_at,'')`

func scanAPIKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	if err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &k, nil
}

// CreateAPIKey stores the hash of a key. The caller keeps the plaintext.
func (db *DB) CreateAPIKey(userID int64, name, prefix, hash, scopes, expiresAt string) (*APIKey, error) {
	var exp any
	if expiresAt != "" {
		exp = expiresAt
	}
	res, err := db.Exec(`INSERT INTO api_keys(user_id, name, prefix, key_hash, scopes, created_at, expires_at) VALUES(?,?,?,?,?,?,?)`,
		userID, name, prefix, hash, scopes, Now(), exp)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanAPIKey(db.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id))
}

// UserByAPIKeyHash resolves a key hash to its owner, rejecting revoked and
// expired keys, and records the use.
func (db *DB) UserByAPIKeyHash(hash string) (*User, *APIKey, error) {
	row := db.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash = ?`, hash)
	k, err := scanAPIKey(row)
	if err != nil {
		return nil, nil, err
	}
	if k.RevokedAt != "" {
		return nil, nil, ErrNotFound
	}
	if k.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, k.ExpiresAt); err == nil && time.Now().UTC().After(exp) {
			return nil, nil, ErrNotFound
		}
	}
	u, err := db.UserByID(k.UserID)
	if err != nil {
		return nil, nil, err
	}
	// Best-effort; a failure to record usage must not fail the request.
	db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, Now(), k.ID)
	return u, k, nil
}

func (db *DB) ListAPIKeys(userID int64) ([]APIKey, error) {
	rows, err := db.Query(`SELECT `+apiKeyCols+` FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (db *DB) RevokeAPIKey(userID, id int64) error {
	res, err := db.Exec(`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, Now(), id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Invites
// ---------------------------------------------------------------------------

func (db *DB) CreateInvite(code string, createdBy int64, email, note, expiresAt string) (*Invite, error) {
	var exp any
	if expiresAt != "" {
		exp = expiresAt
	}
	_, err := db.Exec(`INSERT INTO invites(code, created_by, email, note, created_at, expires_at) VALUES(?,?,?,?,?,?)`,
		code, createdBy, email, note, Now(), exp)
	if err != nil {
		return nil, err
	}
	return db.Invite(code)
}

func (db *DB) Invite(code string) (*Invite, error) {
	var i Invite
	var createdBy, usedBy sql.NullInt64
	err := db.QueryRow(`SELECT code, created_by, email, note, created_at, COALESCE(expires_at,''), COALESCE(used_at,''), used_by FROM invites WHERE code = ?`, code).
		Scan(&i.Code, &createdBy, &i.Email, &i.Note, &i.CreatedAt, &i.ExpiresAt, &i.UsedAt, &usedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if createdBy.Valid {
		i.CreatedBy = &createdBy.Int64
	}
	if usedBy.Valid {
		i.UsedBy = &usedBy.Int64
	}
	return &i, nil
}

// RedeemInvite marks a code used. It fails if the code is unknown, already
// used, or expired.
func (db *DB) RedeemInvite(code string, userID int64) error {
	inv, err := db.Invite(code)
	if err != nil {
		return err
	}
	if inv.UsedAt != "" {
		return errors.New("invite code has already been used")
	}
	if inv.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, inv.ExpiresAt); err == nil && time.Now().UTC().After(exp) {
			return errors.New("invite code has expired")
		}
	}
	_, err = db.Exec(`UPDATE invites SET used_at = ?, used_by = ? WHERE code = ?`, Now(), userID, code)
	return err
}

func (db *DB) ListInvites() ([]Invite, error) {
	rows, err := db.Query(`SELECT code, created_by, email, note, created_at, COALESCE(expires_at,''), COALESCE(used_at,''), used_by FROM invites ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		var i Invite
		var createdBy, usedBy sql.NullInt64
		if err := rows.Scan(&i.Code, &createdBy, &i.Email, &i.Note, &i.CreatedAt, &i.ExpiresAt, &i.UsedAt, &usedBy); err != nil {
			return nil, err
		}
		if createdBy.Valid {
			i.CreatedBy = &createdBy.Int64
		}
		if usedBy.Valid {
			i.UsedBy = &usedBy.Int64
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
