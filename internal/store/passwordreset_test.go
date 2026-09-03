package store

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordResetLifecycle(t *testing.T) {
	db := open(t)
	u, err := db.CreateUser("reset@example.com", "Reset", "old-hash", "member")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.CreateSession("sess-token", u.ID, time.Hour, "test"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := db.CreatePasswordReset(u.ID, "tok-one", time.Hour, "1.2.3.4"); err != nil {
		t.Fatalf("create reset: %v", err)
	}

	// Asking again must retire the first link, or a stale mail stays usable
	// long after somebody assumed it was replaced.
	if _, err := db.CreatePasswordReset(u.ID, "tok-two", time.Hour, "1.2.3.4"); err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if _, err := db.ConsumePasswordReset("tok-one", "new-hash"); err != ErrResetInvalid {
		t.Errorf("the superseded token still worked: %v", err)
	}

	got, err := db.ConsumePasswordReset("tok-two", "new-hash")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got != u.ID {
		t.Errorf("consumed for user %d, want %d", got, u.ID)
	}

	// A reset is what somebody does when they fear the account is taken, so
	// every session must be gone with the old password.
	if _, err := db.UserBySession("sess-token"); err == nil {
		t.Error("a session survived the password reset")
	}

	// Single use.
	if _, err := db.ConsumePasswordReset("tok-two", "newer-hash"); err != ErrResetInvalid {
		t.Errorf("the token was reusable: %v", err)
	}

	// Unknown and expired look identical to a caller.
	if _, err := db.ConsumePasswordReset("never-issued", "h"); err != ErrResetInvalid {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := db.CreatePasswordReset(u.ID, "tok-old", -time.Minute, ""); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, err := db.ConsumePasswordReset("tok-old", "h"); err != ErrResetInvalid {
		t.Errorf("expired token: %v", err)
	}

	// Only the hash is stored: the token itself must not be recoverable.
	var stored string
	db.QueryRow(`SELECT token_hash FROM password_resets LIMIT 1`).Scan(&stored)
	if strings.Contains(stored, "tok-") {
		t.Error("the reset token is stored in the clear")
	}
}
