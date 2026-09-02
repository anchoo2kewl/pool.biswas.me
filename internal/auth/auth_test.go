package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword("correct horse battery", hash); err != nil {
		t.Errorf("the correct password was rejected: %v", err)
	}
	if err := VerifyPassword("wrong password", hash); err == nil {
		t.Error("a wrong password was accepted")
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same password")
	b, _ := HashPassword("same password")
	if a == b {
		t.Error("two hashes of the same password are identical — the salt is not random")
	}
}

func TestShortPasswordsRejected(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Error("a five-character password was accepted")
	}
}

// An OAuth-only account stores an empty hash. No password may ever match it.
func TestEmptyHashNeverVerifies(t *testing.T) {
	if err := VerifyPassword("", ""); err == nil {
		t.Error("an empty password verified against an empty hash")
	}
	if err := VerifyPassword("anything", ""); err == nil {
		t.Error("a password verified against an empty hash")
	}
}

func TestMalformedHashIsRejectedNotPanicking(t *testing.T) {
	for _, bad := range []string{
		"not-a-hash",
		"$argon2id$",
		"$argon2id$v=19$m=65536,t=2,p=2$notbase64$notbase64",
		"$bcrypt$v=19$m=1,t=1,p=1$YWJj$YWJj",
		"$argon2id$v=19$bad-params$YWJj$YWJj",
	} {
		if err := VerifyPassword("password", bad); err == nil {
			t.Errorf("malformed hash %q was accepted", bad)
		}
	}
}

// Keys minted before go-api took over generation are still verified by hash,
// so the hash has to stay a plain SHA-256 of the whole key string.
func TestHashAPIKeyIsStableSHA256(t *testing.T) {
	const key = APIKeyPrefix + "0123456789abcdef"
	sum := sha256.Sum256([]byte(key))
	if got, want := HashAPIKey(key), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("HashAPIKey = %q, want %q — keys issued before the switch would stop authenticating", got, want)
	}
	if HashAPIKey(key) == key {
		t.Error("the stored hash is the key itself")
	}
}

func TestOAuthStateVerification(t *testing.T) {
	const secret = "test-secret"
	nonce := NewNonce()
	state := SignState(secret, nonce)

	if !VerifyState(secret, state, nonce) {
		t.Error("a freshly signed state failed verification")
	}
	if VerifyState(secret, state, "different-nonce") {
		t.Error("state verified against the wrong nonce — CSRF protection is broken")
	}
	if VerifyState("other-secret", state, nonce) {
		t.Error("state verified under a different secret — the signature is not checked")
	}
	if VerifyState(secret, nonce+".deadbeef", nonce) {
		t.Error("a forged signature verified")
	}
	if VerifyState(secret, "garbage", nonce) {
		t.Error("a malformed state verified")
	}
}

func TestInviteCodeIsUnambiguous(t *testing.T) {
	// The alphabet omits I, L, O and U so a handwritten code cannot be misread.
	for i := 0; i < 100; i++ {
		code := NewInviteCode()
		if len(code) != 11 || code[5] != '-' {
			t.Fatalf("code %q is not in XXXXX-XXXXX form", code)
		}
		if strings.ContainsAny(code, "ILOU") {
			t.Fatalf("code %q contains an ambiguous character", code)
		}
	}
}
