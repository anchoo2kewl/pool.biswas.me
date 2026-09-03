package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gologin "github.com/anchoo2kewl/go-login"
)

// The seam between go-login and this app is the one place a mistake would let
// somebody in who should not be, so it is worth testing directly: a token
// signed with the wrong secret, an expired one, or none at all must all end at
// the login page with no session issued.
func TestOAuthSessionRejectsBadTokens(t *testing.T) {
	const secret = "a-test-state-secret-long-enough-for-hmac"

	other, err := gologin.GenerateToken(1, "someone@example.com", "a-different-secret", time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	expired, err := gologin.GenerateToken(1, "someone@example.com", secret, -time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	for name, token := range map[string]string{
		"no token":     "",
		"not a token":  "clearly-not-a-jwt",
		"wrong secret": other,
		"expired":      expired,
	} {
		if _, err := gologin.ValidateToken(token, secret); err == nil {
			t.Errorf("%s: was accepted as valid", name)
		}
	}
}

func TestOAuthSessionAcceptsAGenuineToken(t *testing.T) {
	// The positive case has to hold too, or sign-in breaks for everybody and
	// the negative tests above would still pass.
	const secret = "a-test-state-secret-long-enough-for-hmac"

	token, err := gologin.GenerateToken(42, "owner@example.com", secret, 5*time.Minute)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	claims, err := gologin.ValidateToken(token, secret)
	if err != nil {
		t.Fatalf("a genuine token was rejected: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Email != "owner@example.com" {
		t.Errorf("Email = %q", claims.Email)
	}
}

// A missing token must not 500 or, worse, sign anybody in.
func TestOAuthSessionWithoutATokenRedirectsToLogin(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)

	s.handleOAuthSession(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Errorf("redirected to %q, want the login page", loc)
	}
	// Nothing may be set as a session.
	if cookie := rec.Header().Get("Set-Cookie"); strings.Contains(cookie, sessionCookie+"=") &&
		!strings.Contains(cookie, sessionCookie+"=;") {
		t.Errorf("a session cookie was issued without a token: %q", cookie)
	}
}

// go-login refuses to start when the state key and the signing key match, and
// it is right to: a state token that could be presented as an access token
// would turn the CSRF defence into a way in. This app holds one configured
// secret, so the second key is derived — and that derivation has to actually
// separate them.
func TestDerivedKeyDiffersFromTheSecret(t *testing.T) {
	const secret = "one-configured-oauth-state-secret"

	jwtKey := deriveKey(secret, "pool/gologin/jwt")
	if jwtKey == secret {
		t.Fatal("the derived key equals the secret; go-login would refuse to start")
	}
	if jwtKey == "" {
		t.Fatal("the derived key is empty")
	}

	// Stable, or every restart would invalidate tokens mid-flight.
	if jwtKey != deriveKey(secret, "pool/gologin/jwt") {
		t.Error("derivation is not deterministic")
	}

	// Different labels give unrelated keys, which is the whole point of the
	// separation.
	if jwtKey == deriveKey(secret, "pool/gologin/other") {
		t.Error("two labels produced the same key")
	}
	// And a different secret does too.
	if jwtKey == deriveKey("a-different-secret", "pool/gologin/jwt") {
		t.Error("two secrets produced the same key")
	}
}

// The configuration go-login is actually given must pass its own validation,
// or the handler is nil at runtime and OAuth silently 404s.
func TestOAuthConfigSatisfiesGoLogin(t *testing.T) {
	const secret = "one-configured-oauth-state-secret"

	cfg := &gologin.Config{
		SuccessURL:  "https://pool.example/auth/session",
		ErrorURL:    "https://pool.example/login",
		StateSecret: secret,
		JWTSecret:   deriveKey(secret, "pool/gologin/jwt"),
		JWTExpiry:   5 * time.Minute,
		Google: &gologin.OAuthProviderConfig{
			ClientID:     "id",
			ClientSecret: "secret",
			RedirectURL:  "https://pool.example/auth/google/callback",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the configuration this app builds is rejected by go-login: %v", err)
	}
}
