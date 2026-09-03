package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// ErrResetInvalid is returned for a token that is unknown, already used, or
// past its expiry.
//
// The three are deliberately one error. Telling a caller which of them applies
// says whether a token was ever real, and that is not something anyone holding
// a wrong token needs to know.
var ErrResetInvalid = errors.New("this reset link is invalid or has expired")

// HashResetToken is what gets stored. Plain SHA-256 is right here for the same
// reason as an API key: the input is high-entropy random, so there is no
// dictionary to attack and no work factor worth paying on every check.
func HashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreatePasswordReset records a reset request and returns when it expires.
//
// Any outstanding token for the same account is dropped first: asking for a
// second link should retire the first, or a stale mail left in an inbox stays
// usable long after somebody assumed it was replaced.
func (db *DB) CreatePasswordReset(userID int64, token string, ttl time.Duration, ip string) (time.Time, error) {
	if _, err := db.Exec(`DELETE FROM password_resets WHERE user_id = ?`, userID); err != nil {
		return time.Time{}, err
	}
	expires := time.Now().UTC().Add(ttl)
	_, err := db.Exec(`INSERT INTO password_resets(token_hash, user_id, created_at, expires_at, requested_ip)
		VALUES(?,?,?,?,?)`,
		HashResetToken(token), userID, Now(), expires.Format(time.RFC3339), ip)
	return expires, err
}

// LastPasswordResetAt reports when a reset was last requested for an account,
// so a flood of requests can be slowed without telling the caller anything.
func (db *DB) LastPasswordResetAt(userID int64) (time.Time, bool) {
	var created string
	err := db.QueryRow(`SELECT created_at FROM password_resets
		WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, userID).Scan(&created)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, created)
	return t, err == nil
}

// ConsumePasswordReset verifies a token, sets the new password, and clears
// every session for that account.
//
// It all happens together on purpose. A reset is what somebody does when they
// think the account is compromised, and leaving the attacker's existing
// session alive would defeat the point of it.
func (db *DB) ConsumePasswordReset(token, passwordHash string) (int64, error) {
	var (
		userID  int64
		expires string
		used    string
	)
	err := db.QueryRow(`SELECT user_id, expires_at, used_at FROM password_resets WHERE token_hash = ?`,
		HashResetToken(token)).Scan(&userID, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrResetInvalid
	}
	if err != nil {
		return 0, err
	}
	if used != "" {
		return 0, ErrResetInvalid
	}
	if at, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().UTC().After(at) {
		return 0, ErrResetInvalid
	}

	if err := db.SetPassword(userID, passwordHash); err != nil {
		return 0, err
	}
	if _, err := db.Exec(`UPDATE password_resets SET used_at = ? WHERE token_hash = ?`,
		Now(), HashResetToken(token)); err != nil {
		return 0, err
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return 0, err
	}
	return userID, nil
}
