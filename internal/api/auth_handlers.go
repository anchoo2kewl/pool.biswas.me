package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	gologin "github.com/anchoo2kewl/go-login"
	"github.com/biswas-dev/pool/internal/auth"

	"github.com/biswas-dev/pool/internal/config"
	"github.com/biswas-dev/pool/internal/demo"
	"github.com/biswas-dev/pool/internal/store"
)

func withUser(r *http.Request, u *store.User) context.Context {
	return context.WithValue(r.Context(), userKey, u)
}

// handleClientConfig tells the browser which sign-in options exist, so the
// login page only renders buttons that will actually work.
func (s *Server) handleClientConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"registration":   string(s.Cfg.Registration),
		"github_enabled": s.Cfg.GitHubEnabled(),
		"google_enabled": s.Cfg.GoogleEnabled(),
		"ai_enabled":     s.Cfg.AIEnabled(),
		"env":            s.Cfg.Env,
		"demo_enabled":   s.Cfg.DemoEnabled,
		// Published on purpose: this is the point of a demo account.
		"demo_email":    s.Cfg.DemoEmail,
		"demo_password": s.Cfg.DemoPassword,
	})
}

type registerRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Invite   string `json:"invite"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}

	switch s.Cfg.Registration {
	case config.RegistrationClosed:
		writeError(w, http.StatusForbidden, "registration is closed")
		return
	case config.RegistrationInvite:
		if req.Invite == "" {
			writeError(w, http.StatusForbidden, "an invite code is required")
			return
		}
		if _, err := s.DB.Invite(strings.ToUpper(strings.TrimSpace(req.Invite))); err != nil {
			writeError(w, http.StatusForbidden, "invalid invite code")
			return
		}
	}

	if _, err := s.DB.UserByEmail(req.Email); err == nil {
		writeError(w, http.StatusConflict, "an account with that email already exists")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	role := "member"
	// The first account to exist owns the instance.
	if n, err := s.DB.CountUsers(); err == nil && n == 0 {
		role = "admin"
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.SplitN(req.Email, "@", 2)[0]
	}
	user, err := s.DB.CreateUser(req.Email, name, hash, role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.Cfg.Registration == config.RegistrationInvite {
		if err := s.DB.RedeemInvite(strings.ToUpper(strings.TrimSpace(req.Invite)), user.ID); err != nil {
			log.Printf("redeem invite: %v", err)
		}
	}

	s.startSession(w, r, user)
	writeJSON(w, http.StatusCreated, user)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, hash, err := s.DB.PasswordHash(req.Email)
	if err != nil {
		// Same message and rough timing whether the account exists or not.
		auth.VerifyPassword(req.Password, "$argon2id$v=19$m=65536,t=2,p=2$YWJjZGVmZ2hpamtsbW5vcA$YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXphYmNkZWY")
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if hash == "" {
		writeError(w, http.StatusUnauthorized, "this account signs in with Google or GitHub")
		return
	}
	if err := auth.VerifyPassword(req.Password, hash); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	user, err := s.DB.UserByID(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.startSession(w, r, user)
	writeJSON(w, http.StatusOK, user)
}

// handleDemoLogin signs the visitor straight into the demo account, so the
// landing page can offer a single button rather than asking them to copy
// credentials.
func (s *Server) handleDemoLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.DemoEnabled {
		writeError(w, http.StatusNotFound, "the demo is not enabled on this instance")
		return
	}
	user, err := s.DB.UserByEmail(s.Cfg.DemoEmail)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "the demo account is not ready yet")
		return
	}
	s.startSession(w, r, user)
	writeJSON(w, http.StatusOK, user)
}

// handleDemoReset rebuilds the demo data on request, so a visitor who has made
// a mess can put it back without waiting for the timer.
func (s *Server) handleDemoReset(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if !s.Cfg.DemoEnabled || u.Email != s.Cfg.DemoEmail {
		writeError(w, http.StatusForbidden, "only the demo account can be reset")
		return
	}
	if err := demo.Reset(s.DB, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "demo data reset"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.DB.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.Cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	token := auth.NewSessionToken()
	ttl := time.Duration(s.Cfg.SessionTTLH) * time.Hour
	if err := s.DB.CreateSession(token, u.ID, ttl, r.UserAgent()); err != nil {
		log.Printf("create session: %v", err)
		return
	}
	s.DB.TouchLogin(u.ID)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(ttl.Seconds()), HttpOnly: true,
		Secure: s.Cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

// ── OAuth ────────────────────────────────────────────────────────────────

// oauthHandler builds the go-login handler, or nil when no provider is
// configured.
//
// go-login owns the whole OAuth flow now — signed state, the token exchange,
// the profile fetch, and resolving or creating the account through
// gologinStore. This app previously carried its own copy of all of that, and
// two implementations of the same protocol across two apps is one more than
// anybody wants to keep correct.
func (s *Server) oauthHandler() *gologin.Handler {
	if s.oauth != nil {
		return s.oauth
	}
	if !s.Cfg.GoogleEnabled() && !s.Cfg.GitHubEnabled() {
		return nil
	}

	cfg := &gologin.Config{
		// go-login finishes by redirecting here with a short-lived JWT. Pool
		// exchanges it for its own session immediately, because a server-side
		// session can be revoked and a JWT cannot — that is worth keeping.
		SuccessURL:  s.Cfg.AppURL + "/auth/session",
		ErrorURL:    s.Cfg.AppURL + "/login",
		StateSecret: s.Cfg.OAuthStateSecret,
		// Derived rather than reused. go-login refuses to start if these two
		// match, and it is right to: a state token that could be presented as
		// an access token would turn the CSRF defence into a way in. One
		// configured secret, two keys, separated by their labels.
		JWTSecret: deriveKey(s.Cfg.OAuthStateSecret, "pool/gologin/jwt"),
		// Only long enough to survive the redirect back.
		JWTExpiry: 5 * time.Minute,
	}
	if s.Cfg.GoogleEnabled() {
		cfg.Google = &gologin.OAuthProviderConfig{
			ClientID:     s.Cfg.GoogleClientID,
			ClientSecret: s.Cfg.GoogleClientSecret,
			RedirectURL:  s.Cfg.AppURL + "/auth/google/callback",
		}
	}
	if s.Cfg.GitHubEnabled() {
		cfg.GitHub = &gologin.OAuthProviderConfig{
			ClientID:     s.Cfg.GitHubClientID,
			ClientSecret: s.Cfg.GitHubClientSecret,
			RedirectURL:  s.Cfg.AppURL + "/auth/github/callback",
		}
	}

	h, err := gologin.NewHandler(cfg, gologinStore{s})
	if err != nil {
		log.Printf("oauth: %v", err)
		return nil
	}
	s.oauth = h
	return h
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	h := s.oauthHandler()
	if h == nil {
		http.Error(w, "sign-in with that provider is not configured", http.StatusNotFound)
		return
	}
	switch r.PathValue("provider") {
	case "google":
		h.HandleGoogleInitiate(w, r)
	case "github":
		h.HandleGithubInitiate(w, r)
	default:
		http.Error(w, "unknown provider", http.StatusNotFound)
	}
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	h := s.oauthHandler()
	if h == nil {
		http.Error(w, "sign-in with that provider is not configured", http.StatusNotFound)
		return
	}
	switch r.PathValue("provider") {
	case "google":
		h.HandleGoogleCallback(w, r)
	case "github":
		h.HandleGithubCallback(w, r)
	default:
		http.Error(w, "unknown provider", http.StatusNotFound)
	}
}

// handleOAuthSession turns go-login's token into a pool session.
//
// This is the seam between the two models. go-login hands back a JWT, which is
// fine for the two seconds it spends in a redirect but is not what this app
// wants people carrying around: a session row can be listed, expired and
// revoked, and the whole front end already speaks cookies. So the token is
// spent here, once, and never reaches the client.
func (s *Server) handleOAuthSession(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		log.Printf("oauth session: %s", msg)
		http.Redirect(w, r, "/login?error="+urlEncode(msg), http.StatusFound)
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		fail("sign-in did not complete, please try again")
		return
	}
	claims, err := gologin.ValidateToken(token, deriveKey(s.Cfg.OAuthStateSecret, "pool/gologin/jwt"))
	if err != nil {
		fail("sign-in could not be verified, please try again")
		return
	}
	user, err := s.DB.UserByID(claims.UserID)
	if err != nil {
		fail("that account is no longer available")
		return
	}

	s.startSession(w, r, user)
	http.Redirect(w, r, "/app", http.StatusFound)
}

func urlEncode(s string) string {
	return strings.NewReplacer(" ", "%20", "&", "%26", "?", "%3F", "#", "%23", ",", "%2C").Replace(s)
}

// ── Account ──────────────────────────────────────────────────────────────

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	providers, _ := s.DB.ListIdentities(u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":      u,
		"providers": providers,
		"ai": map[string]any{
			"configured":    u.AIAPIKey != "" || s.Cfg.AIEnabled(),
			"using_own_key": u.AIAPIKey != "",
			"model":         firstNonEmpty(u.AIModel, s.Cfg.AIModel),
			"base_url":      firstNonEmpty(u.AIBaseURL, s.Cfg.AIBaseURL),
		},
	})
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) != "" {
		if _, err := s.DB.Exec(`UPDATE users SET name = ? WHERE id = ?`, strings.TrimSpace(req.Name), u.ID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.Password != "" {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.DB.SetPassword(u.ID, hash); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	updated, err := s.DB.UserByID(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleSetAISettings stores the caller's own LLM credentials, so insights are
// billed to them rather than to the server's shared key.
func (s *Server) handleSetAISettings(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.SetAISettings(u.ID, strings.TrimSpace(req.APIKey), strings.TrimSpace(req.BaseURL), strings.TrimSpace(req.Model)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// deriveKey produces a distinct key from one configured secret.
//
// HMAC with a label gives domain separation: the same secret yields unrelated
// keys for unrelated purposes, so a token signed for one can never be verified
// as the other. That is what lets this app hold a single OAuth secret in its
// configuration and still satisfy go-login's insistence that the state key and
// the signing key differ.
func deriveKey(secret, label string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(label))
	return hex.EncodeToString(mac.Sum(nil))
}
