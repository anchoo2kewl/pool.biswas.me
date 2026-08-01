package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/config"
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

func (s *Server) provider(name string) *auth.Provider {
	switch name {
	case "github":
		if s.Cfg.GitHubEnabled() {
			return auth.GitHub(s.Cfg.GitHubClientID, s.Cfg.GitHubClientSecret)
		}
	case "google":
		if s.Cfg.GoogleEnabled() {
			return auth.Google(s.Cfg.GoogleClientID, s.Cfg.GoogleClientSecret)
		}
	}
	return nil
}

func (s *Server) redirectURI(name string) string {
	return s.Cfg.AppURL + "/auth/" + name + "/callback"
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p := s.provider(name)
	if p == nil {
		http.Error(w, "sign-in with "+name+" is not configured", http.StatusNotFound)
		return
	}

	nonce := auth.NewNonce()
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_nonce", Value: nonce, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: s.Cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthCodeURL(s.redirectURI(name), auth.SignState(s.Cfg.OAuthStateSecret, nonce)), http.StatusFound)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p := s.provider(name)
	if p == nil {
		http.Error(w, "sign-in with "+name+" is not configured", http.StatusNotFound)
		return
	}

	fail := func(msg string) {
		log.Printf("oauth %s: %s", name, msg)
		http.Redirect(w, r, "/login?error="+urlEncode(msg), http.StatusFound)
	}

	if e := r.URL.Query().Get("error"); e != "" {
		fail("sign-in was cancelled")
		return
	}
	nonceCookie, err := r.Cookie("oauth_nonce")
	if err != nil {
		fail("sign-in expired, please try again")
		return
	}
	if !auth.VerifyState(s.Cfg.OAuthStateSecret, r.URL.Query().Get("state"), nonceCookie.Value) {
		fail("sign-in could not be verified, please try again")
		return
	}
	// The nonce is single-use.
	http.SetCookie(w, &http.Cookie{Name: "oauth_nonce", Value: "", Path: "/", MaxAge: -1})

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	token, err := p.Exchange(ctx, r.URL.Query().Get("code"), s.redirectURI(name))
	if err != nil {
		fail("could not complete sign-in with " + name)
		return
	}
	id, err := p.Identity(ctx, token)
	if err != nil {
		fail(err.Error())
		return
	}

	user, err := s.linkOrCreateUser(id)
	if err != nil {
		fail(err.Error())
		return
	}
	s.startSession(w, r, user)
	http.Redirect(w, r, "/app", http.StatusFound)
}

// linkOrCreateUser resolves an OAuth identity to an account: an existing link,
// then a matching verified email, then a brand-new account.
func (s *Server) linkOrCreateUser(id *auth.Identity) (*store.User, error) {
	if u, err := s.DB.UserByIdentity(id.Provider, id.UID); err == nil {
		s.DB.LinkIdentity(id.Provider, id.UID, u.ID, id.Email, id.AvatarURL)
		return u, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// An existing password account with the same verified email adopts this
	// identity, so a user who signed up with email can later use Google.
	if u, err := s.DB.UserByEmail(id.Email); err == nil {
		if err := s.DB.LinkIdentity(id.Provider, id.UID, u.ID, id.Email, id.AvatarURL); err != nil {
			return nil, err
		}
		return u, nil
	}

	if s.Cfg.Registration == config.RegistrationClosed {
		return nil, errors.New("registration is closed")
	}

	role := "member"
	if n, err := s.DB.CountUsers(); err == nil && n == 0 {
		role = "admin"
	}
	// OAuth accounts have no password; the column is not nullable, so store a
	// value that no password hash can ever equal.
	u, err := s.DB.CreateUser(id.Email, id.Name, "", role)
	if err != nil {
		return nil, err
	}
	if err := s.DB.LinkIdentity(id.Provider, id.UID, u.ID, id.Email, id.AvatarURL); err != nil {
		return nil, err
	}
	return u, nil
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

// ── API keys ─────────────────────────────────────────────────────────────

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.DB.ListAPIKeys(userFrom(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Name      string `json:"name"`
		Scopes    string `json:"scopes"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "API key"
	}
	if req.Scopes == "" {
		req.Scopes = "read,write"
	}

	key, prefix, hash := auth.NewAPIKey()
	created, err := s.DB.CreateAPIKey(u.ID, req.Name, prefix, hash, req.Scopes, req.ExpiresAt)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The plaintext key is returned exactly once, here.
	created.Key = key
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	if err := s.DB.RevokeAPIKey(userFrom(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
