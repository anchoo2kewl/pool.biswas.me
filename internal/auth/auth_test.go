package auth

import (
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

func TestAPIKeyShape(t *testing.T) {
	key, prefix, hash := NewAPIKey()
	if !strings.HasPrefix(key, APIKeyPrefix) {
		t.Errorf("key %q does not start with %q", key, APIKeyPrefix)
	}
	if !strings.HasPrefix(key, prefix) {
		t.Errorf("prefix %q is not a prefix of key %q", prefix, key)
	}
	if len(prefix) >= len(key) {
		t.Error("the displayed prefix is the whole key")
	}
	if hash == key {
		t.Error("the stored hash is the key itself")
	}
	if HashAPIKey(key) != hash {
		t.Error("hashing the key does not reproduce the stored hash")
	}
}

func TestAPIKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		key, _, _ := NewAPIKey()
		if seen[key] {
			t.Fatal("NewAPIKey returned a duplicate")
		}
		seen[key] = true
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
