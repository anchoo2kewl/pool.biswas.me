package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	gologin "github.com/anchoo2kewl/go-login"

	"github.com/biswas-dev/pool/internal/store"
)

// ceremonyTTL bounds a WebAuthn exchange. The browser prompt is in front of
// the person the whole time, so this only has to outlast finding the key.
const ceremonyTTL = 5 * time.Minute

// passkeyStore adapts this app's tables to go-login's two reads.
//
// Credentials are stored base64-encoded because they land in text columns; the
// library works in bytes, so the seam converts.
type passkeyStore struct{ db *store.DB }

func (p passkeyStore) PasskeyCredentials(_ context.Context, userID int64) ([]gologin.PasskeyCredential, error) {
	stored, err := p.db.ListPasskeys(userID)
	if err != nil {
		return nil, err
	}
	out := make([]gologin.PasskeyCredential, 0, len(stored))
	for _, k := range stored {
		id, err := base64.RawURLEncoding.DecodeString(k.CredentialID)
		if err != nil {
			log.Printf("passkey %d has an unreadable credential id: %v", k.ID, err)
			continue
		}
		key, err := base64.StdEncoding.DecodeString(k.PublicKey)
		if err != nil {
			log.Printf("passkey %d has an unreadable public key: %v", k.ID, err)
			continue
		}
		out = append(out, gologin.PasskeyCredential{
			ID: id, PublicKey: key,
			SignCount: uint32(k.SignCount), BackedUp: k.BackedUp,
			AttestationType: k.Attestation,
		})
	}
	return out, nil
}

func (p passkeyStore) PasskeyUserByID(_ context.Context, userID int64) (gologin.PasskeyUser, error) {
	u, err := p.db.UserByID(userID)
	if err != nil {
		return gologin.PasskeyUser{}, gologin.ErrPasskeyUnknownUser
	}
	return gologin.PasskeyUser{ID: u.ID, Email: u.Email, DisplayName: u.Name}, nil
}

// passkeys builds the relying party for this instance.
func (s *Server) passkeys() (*gologin.Passkeys, error) {
	return gologin.NewPasskeys(gologin.PasskeyConfig{
		DisplayName: "Pool",
		AppURL:      s.Cfg.AppURL,
	}, passkeyStore{s.DB})
}

// webAuthnConfigured reports whether a browser will permit passkeys here.
func (s *Server) webAuthnConfigured() bool { return gologin.PasskeysUsable(s.Cfg.AppURL) }

// handlePasskeyRegisterBegin starts adding a passkey to the signed-in account.
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	pk, err := s.passkeys()
	if err != nil {
		log.Printf("passkey config: %v", err)
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}

	options, session, err := pk.BeginRegistration(r.Context(), passkeyUserOf(u))
	if err != nil {
		log.Printf("begin passkey registration: %v", err)
		writeError(w, http.StatusInternalServerError, "could not start passkey setup")
		return
	}
	token, err := s.storeCeremony(store.ChallengeWebAuthnRegister, &u.ID, session)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge": token, "options": options})
}

// handlePasskeyRegisterFinish records the new credential.
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Challenge string          `json:"challenge"`
		Name      string          `json:"name"`
		Response  json.RawMessage `json:"response"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	session, err := s.takeCeremony(store.ChallengeWebAuthnRegister, req.Challenge, &u.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pk, err := s.passkeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}

	cred, err := pk.FinishRegistration(r.Context(), passkeyUserOf(u), session, req.Response)
	if err != nil {
		// The wrapped message names the check that failed — origin, RP id,
		// challenge — which is what makes this debuggable from the log.
		log.Printf("create passkey credential: %v", err)
		writeError(w, http.StatusBadRequest, "that passkey could not be verified")
		return
	}

	saved, err := s.DB.AddPasskey(&store.Passkey{
		UserID:       u.ID,
		CredentialID: base64.RawURLEncoding.EncodeToString(cred.ID),
		PublicKey:    base64.StdEncoding.EncodeToString(cred.PublicKey),
		Attestation:  cred.AttestationType,
		Transports:   strings.Join(cred.Transports, ","),
		SignCount:    int64(cred.SignCount),
		BackedUp:     cred.BackedUp,
		Name:         passkeyName(req.Name),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "that passkey is already registered")
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// handlePasskeyLoginBegin starts a sign-in with no password and no email.
func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	pk, err := s.passkeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}
	options, session, err := pk.BeginLogin(r.Context())
	if err != nil {
		log.Printf("begin passkey login: %v", err)
		writeError(w, http.StatusInternalServerError, "could not start passkey sign-in")
		return
	}
	token, err := s.storeCeremony(store.ChallengeWebAuthnLogin, nil, session)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge": token, "options": options})
}

// handlePasskeyLoginFinish verifies the assertion and starts a session.
func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string          `json:"challenge"`
		Response  json.RawMessage `json:"response"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, err := s.takeCeremony(store.ChallengeWebAuthnLogin, req.Challenge, nil)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	pk, err := s.passkeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}

	who, cred, err := pk.FinishLogin(r.Context(), session, req.Response)
	if err != nil {
		if errors.Is(err, gologin.ErrPasskeyCloned) {
			log.Printf("passkey clone warning for user %d", who.ID)
		} else {
			log.Printf("validate passkey login: %v", err)
		}
		writeError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}

	user, err := s.DB.UserByID(who.ID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}
	if stored, err := s.DB.PasskeyByCredentialID(base64.RawURLEncoding.EncodeToString(cred.ID)); err == nil {
		s.DB.TouchPasskey(stored.ID, int64(cred.SignCount))
	}

	// A passkey is already two factors — something you have, unlocked by
	// something you are or know — so it stands on its own without the code.
	s.startSession(w, r, user)
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleRenamePasskey(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid passkey id")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.RenamePasskey(u.ID, id, passkeyName(req.Name)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid passkey id")
		return
	}
	if err := s.DB.DeletePasskey(u.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Ceremony state ───────────────────────────────────────────────────────

func (s *Server) storeCeremony(kind string, userID *int64, session []byte) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	return token, s.DB.CreateChallenge(token, kind, userID, string(session), ceremonyTTL)
}

func (s *Server) takeCeremony(kind, token string, wantUser *int64) ([]byte, error) {
	owner, blob, err := s.DB.TakeChallenge(strings.TrimSpace(token), kind)
	if err != nil {
		return nil, err
	}
	// A registration ceremony belongs to the account that started it. Letting
	// one account finish another's would attach a passkey to the wrong user.
	if wantUser != nil && (owner == nil || *owner != *wantUser) {
		return nil, store.ErrChallengeInvalid
	}
	return []byte(blob), nil
}

func passkeyUserOf(u *store.User) gologin.PasskeyUser {
	return gologin.PasskeyUser{ID: u.ID, Email: u.Email, DisplayName: u.Name}
}

// passkeyName bounds the label and gives an unnamed key something to be listed
// as.
func passkeyName(name string) string {
	name = trimTo(name, 60)
	if name == "" {
		return "Passkey"
	}
	return name
}
