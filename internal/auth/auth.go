// Package auth handles credentials: password hashing, session cookies, API
// keys, and OAuth state signing.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrBadCredentials is returned when a password does not match.
var ErrBadCredentials = errors.New("invalid email or password")

// argon2id parameters. These target roughly 60ms on a small VM — enough to
// make offline cracking expensive without hurting login latency.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword returns an encoded argon2id hash, salt included.
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an encoded hash in constant time.
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ErrBadCredentials
	}
	var version, memory, time, threads int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return ErrBadCredentials
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return ErrBadCredentials
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ErrBadCredentials
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return ErrBadCredentials
	}
	got := argon2.IDKey([]byte(password), salt, uint32(time), uint32(memory), uint8(threads), uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrBadCredentials
	}
	return nil
}

// NewSessionToken returns a random, URL-safe session identifier.
func NewSessionToken() string { return randomToken(32) }

// APIKeyPrefix is the identifying prefix on every issued key.
//
// Minting is go-api's job now (see internal/api.TokenScheme); what stays here
// is the prefix both agree on and the hash, which keys issued before that
// switch are still verified with.
const APIKeyPrefix = "pool_sk_"

// HashAPIKey is a plain SHA-256 of the key. API keys are high-entropy random
// values, so a slow KDF buys nothing and would cost a hash on every request.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// NewInviteCode returns a short, human-typable invite code.
func NewInviteCode() string {
	// Crockford-style alphabet: no I, L, O, U, so codes cannot be misread.
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic("auth: cannot read random bytes: " + err.Error())
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out[:5]) + "-" + string(out[5:])
}

// SignState produces an HMAC-signed OAuth state value carrying a nonce.
func SignState(secret, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nonce))
	return nonce + "." + hex.EncodeToString(mac.Sum(nil))
}

// VerifyState checks a state value against the nonce stored in the cookie.
func VerifyState(secret, state, nonce string) bool {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 || parts[0] != nonce {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nonce))
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(want))
}

// NewNonce returns a random value for OAuth state.
func NewNonce() string { return randomToken(16) }

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: cannot read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
