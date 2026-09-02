package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	goapi "github.com/anchoo2kewl/go-api"

	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/store"
)

// TokenScheme is this application's slice of the shared go-api token format.
// The prefix is what makes a pool key recognisable on sight, wherever it turns
// up — a log line, a shell history, a pasted config.
var TokenScheme = goapi.NewScheme(auth.APIKeyPrefix)

// tokenStore adapts the api_keys table to go-api's Store.
//
// The type parameter is the subject the app identifies callers by, which here
// is the user's integer id.
type tokenStore struct{ db *store.DB }

func (t tokenStore) Lookup(ctx context.Context, hash string) (goapi.Record, int64, error) {
	var (
		userID            int64
		name, prefix      string
		scopes            string
		revoked, expires  sql.NullString
		lastUsed, created sql.NullString
	)
	err := t.db.QueryRow(`
		SELECT k.user_id, k.name, k.prefix, k.scopes, k.revoked_at, k.expires_at,
		       k.last_used_at, k.created_at
		  FROM api_keys k
		  JOIN users u ON u.id = k.user_id
		 WHERE k.key_hash = ?`, hash).
		Scan(&userID, &name, &prefix, &scopes, &revoked, &expires, &lastUsed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return goapi.Record{}, 0, goapi.ErrNotFound
	}
	if err != nil {
		return goapi.Record{}, 0, err
	}

	rec := goapi.Record{
		Name:   name,
		Prefix: prefix,
		Scopes: goapi.ParseScopes(scopes),
		Active: !revoked.Valid || revoked.String == "",
	}
	if t := parseStamp(expires); t != nil {
		rec.ExpiresAt = t
	}
	if t := parseStamp(lastUsed); t != nil {
		rec.LastUsedAt = t
	}
	if t := parseStamp(created); t != nil {
		rec.CreatedAt = *t
	}
	return rec, userID, nil
}

// Touch records last use. go-api calls it after the decision, so a failure
// here is reported rather than allowed to fail the request.
func (t tokenStore) Touch(ctx context.Context, hash string) error {
	_, err := t.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE key_hash = ?`, store.Now(), hash)
	return err
}

// parseStamp reads one of the timestamps the store writes, which are RFC3339
// strings rather than a native date type.
func parseStamp(v sql.NullString) *time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v.String)
	if err != nil {
		return nil
	}
	return &t
}

// tokenAuthenticator builds the verifier for incoming API keys.
func (s *Server) tokenAuthenticator() *goapi.Authenticator[int64] {
	auth := goapi.NewAuthenticator[int64](TokenScheme, tokenStore{s.DB})
	auth.OnTouchError = func(err error) { log.Printf("recording api key use: %v", err) }
	return auth
}

// authenticateToken verifies an API key and returns its owner.
//
// go-api owns the scheme, the scope rules and the error mapping, so this app
// and 75hard reject the same things for the same reasons — including the part
// that is easy to forget, which is that a read-only key must not be able to
// POST simply because no handler thought to check.
func (s *Server) authenticateToken(r *http.Request, key string) (*store.User, error) {
	userID, _, err := s.tokenAuthenticator().Authenticate(r.Context(), key, r.Method)
	if err != nil {
		return nil, err
	}
	u, err := s.DB.UserByID(userID)
	if err != nil {
		return nil, goapi.ErrSubjectUnavailable
	}
	return u, nil
}

// ── Handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.DB.ListAPIKeys(userFrom(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// handleCreateAPIKey issues a key.
//
// The plaintext is returned exactly once, in this response, and cannot be
// recovered afterwards — only its hash is kept. The response also carries the
// OpenAPI document's location and a working example, because this is the one
// moment the secret exists and a complete command can be produced.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Name string `json:"name"`
		// Scopes is read, or read and write. Anything unrecognised normalises
		// to read: the safe reading of an ambiguous request is the one that
		// cannot change data.
		Scopes    any    `json:"scopes"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A key issued by a key would let a read credential quietly escalate
	// itself into a write one, so this is reachable only with a real session.
	if tokenAuthed(r) {
		writeError(w, http.StatusForbidden, "sign in to create a key; an API key cannot issue another")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "API key"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	scopes := parseScopeInput(req.Scopes)

	cred, err := TokenScheme.Generate()
	if err != nil {
		log.Printf("generate api key: %v", err)
		writeError(w, http.StatusInternalServerError, "could not generate a key")
		return
	}

	created, err := s.DB.CreateAPIKey(u.ID, name, cred.Prefix, cred.Hash, scopes.String(), req.ExpiresAt)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The plaintext key is returned exactly once, here.
	created.Key = cred.Plaintext

	discovery := goapi.NewDiscovery(s.Cfg.AppURL, cred.Plaintext, scopes)
	// go-api's stock example points at an endpoint this app does not have, so
	// it is replaced with one that works as printed.
	discovery.Example = fmt.Sprintf("curl -H 'Authorization: Bearer %s' %s/api/pools",
		cred.Plaintext, discovery.BaseURL)

	writeJSON(w, http.StatusCreated, map[string]any{
		"key":       created,
		"discovery": discovery,
	})
}

// parseScopeInput accepts both shapes the API has ever taken: a comma-joined
// string, which is what the settings page has always sent, and a list, which
// is what go-api's own callers send.
func parseScopeInput(raw any) goapi.Scopes {
	switch v := raw.(type) {
	case string:
		return goapi.ParseScopes(v)
	case []any:
		out := make(goapi.Scopes, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, strings.ToLower(strings.TrimSpace(s)))
			}
		}
		return out.Normalise()
	}
	// Unspecified means a key that can do everything the person could, which
	// is what somebody clicking "create key" in their own account expects.
	return goapi.Scopes{goapi.ScopeRead, goapi.ScopeWrite}
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
