package store

import (
	"database/sql"
	"errors"
	"time"
)

// ── Time-based one-time codes ────────────────────────────────────────────

// SetTOTPSecret stores a secret that has been issued but not yet proved.
//
// It stays unconfirmed until a code computed from it comes back, so an
// interrupted setup cannot lock somebody out of their own account with a
// secret their phone never received.
func (db *DB) SetTOTPSecret(userID int64, secret string) error {
	_, err := db.Exec(`UPDATE users SET totp_secret = ?, totp_confirmed_at = '' WHERE id = ?`,
		secret, userID)
	return err
}

// ConfirmTOTP marks the secret proved, which is what turns the second factor on.
func (db *DB) ConfirmTOTP(userID int64) error {
	_, err := db.Exec(`UPDATE users SET totp_confirmed_at = ? WHERE id = ? AND totp_secret != ''`,
		Now(), userID)
	return err
}

// DisableTOTP removes the second factor and every recovery code with it.
// Leaving the codes behind would keep a way in that the person believes they
// have just turned off.
func (db *DB) DisableTOTP(userID int64) error {
	if _, err := db.Exec(`UPDATE users SET totp_secret = '', totp_confirmed_at = '' WHERE id = ?`, userID); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return err
}

// TOTPSecret returns the stored secret and whether it has been confirmed.
func (db *DB) TOTPSecret(userID int64) (secret string, confirmed bool, err error) {
	var confirmedAt string
	err = db.QueryRow(`SELECT totp_secret, totp_confirmed_at FROM users WHERE id = ?`, userID).
		Scan(&secret, &confirmedAt)
	return secret, confirmedAt != "", err
}

// ── Recovery codes ───────────────────────────────────────────────────────

// ReplaceRecoveryCodes swaps in a fresh set, retiring whatever was there.
func (db *DB) ReplaceRecoveryCodes(userID int64, hashes []string) error {
	if _, err := db.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	now := Now()
	for _, h := range hashes {
		if _, err := db.Exec(`INSERT INTO recovery_codes(user_id, code_hash, created_at) VALUES(?,?,?)`,
			userID, h, now); err != nil {
			return err
		}
	}
	return nil
}

// UseRecoveryCode spends one code, reporting whether it was valid and unused.
//
// The update is the check: marking it used only where it is currently unused
// means two requests racing the same code cannot both succeed.
func (db *DB) UseRecoveryCode(userID int64, hash string) (bool, error) {
	res, err := db.Exec(`UPDATE recovery_codes SET used_at = ?
		WHERE user_id = ? AND code_hash = ? AND used_at = ''`, Now(), userID, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecoveryCodesLeft counts the unused codes, so the interface can say when it
// is time to generate more.
func (db *DB) RecoveryCodesLeft(userID int64) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at = ''`, userID).Scan(&n)
	return n, err
}

// ── Passkeys ─────────────────────────────────────────────────────────────

// Passkey is one registered credential.
type Passkey struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"-"`
	CredentialID string `json:"credential_id"`
	PublicKey    string `json:"-"`
	Attestation  string `json:"-"`
	Transports   string `json:"transports"`
	SignCount    int64  `json:"-"`
	BackedUp     bool   `json:"backed_up"`
	Name         string `json:"name"`
	CreatedAt    string `json:"created_at"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
}

const passkeyCols = `id, user_id, credential_id, public_key, attestation, transports,
	sign_count, backed_up, name, created_at, last_used_at`

func scanPasskey(row interface{ Scan(...any) error }) (*Passkey, error) {
	var p Passkey
	var backed int64
	err := row.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.PublicKey, &p.Attestation,
		&p.Transports, &p.SignCount, &backed, &p.Name, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	p.BackedUp = backed != 0
	return &p, err
}

// AddPasskey records a newly registered credential.
func (db *DB) AddPasskey(p *Passkey) (*Passkey, error) {
	backed := int64(0)
	if p.BackedUp {
		backed = 1
	}
	res, err := db.Exec(`INSERT INTO webauthn_credentials
		(user_id, credential_id, public_key, attestation, transports, sign_count, backed_up, name, created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		p.UserID, p.CredentialID, p.PublicKey, p.Attestation, p.Transports,
		p.SignCount, backed, p.Name, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanPasskey(db.QueryRow(`SELECT `+passkeyCols+` FROM webauthn_credentials WHERE id = ?`, id))
}

// ListPasskeys returns a user's credentials, newest first.
func (db *DB) ListPasskeys(userID int64) ([]Passkey, error) {
	rows, err := db.Query(`SELECT `+passkeyCols+` FROM webauthn_credentials
		WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PasskeyByCredentialID finds a credential and its owner during a sign-in.
func (db *DB) PasskeyByCredentialID(credentialID string) (*Passkey, error) {
	return scanPasskey(db.QueryRow(`SELECT `+passkeyCols+`
		FROM webauthn_credentials WHERE credential_id = ?`, credentialID))
}

// TouchPasskey records a use and the authenticator's signature counter.
//
// The counter is how a cloned authenticator is detected: it only ever moves
// forward on a genuine one.
func (db *DB) TouchPasskey(id int64, signCount int64) error {
	_, err := db.Exec(`UPDATE webauthn_credentials SET last_used_at = ?, sign_count = ? WHERE id = ?`,
		Now(), signCount, id)
	return err
}

// RenamePasskey changes the label somebody gave a key.
func (db *DB) RenamePasskey(userID, id int64, name string) error {
	res, err := db.Exec(`UPDATE webauthn_credentials SET name = ? WHERE id = ? AND user_id = ?`,
		name, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePasskey removes a credential.
func (db *DB) DeletePasskey(userID, id int64) error {
	res, err := db.Exec(`DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Two-request ceremonies ───────────────────────────────────────────────

// Challenge kinds.
const (
	ChallengeMFALogin         = "mfa_login"
	ChallengeWebAuthnRegister = "webauthn_register"
	ChallengeWebAuthnLogin    = "webauthn_login"
)

// ErrChallengeInvalid covers unknown, spent and expired alike, for the same
// reason a reset token does: which one it was is not a caller's business.
var ErrChallengeInvalid = errors.New("this attempt has expired — start again")

// CreateChallenge stores state between the two halves of a ceremony.
func (db *DB) CreateChallenge(token, kind string, userID *int64, data string, ttl time.Duration) error {
	_, err := db.Exec(`INSERT INTO auth_challenges(token, user_id, kind, data, created_at, expires_at)
		VALUES(?,?,?,?,?,?)`,
		token, userID, kind, data, Now(), time.Now().UTC().Add(ttl).Format(time.RFC3339))
	return err
}

// TakeChallenge consumes a challenge, returning who it belongs to and what was
// stored with it. It is single use: the row is deleted whether or not what
// follows succeeds, so a replayed token buys nothing.
func (db *DB) TakeChallenge(token, kind string) (userID *int64, data string, err error) {
	var (
		uid     sql.NullInt64
		expires string
	)
	err = db.QueryRow(`SELECT user_id, data, expires_at FROM auth_challenges WHERE token = ? AND kind = ?`,
		token, kind).Scan(&uid, &data, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrChallengeInvalid
	}
	if err != nil {
		return nil, "", err
	}
	db.Exec(`DELETE FROM auth_challenges WHERE token = ?`, token)

	if at, perr := time.Parse(time.RFC3339, expires); perr != nil || time.Now().UTC().After(at) {
		return nil, "", ErrChallengeInvalid
	}
	if uid.Valid {
		id := uid.Int64
		return &id, data, nil
	}
	return nil, data, nil
}

// PurgeExpiredChallenges clears what nobody came back for.
func (db *DB) PurgeExpiredChallenges() error {
	_, err := db.Exec(`DELETE FROM auth_challenges WHERE expires_at < ?`, Now())
	return err
}
