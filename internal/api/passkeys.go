package api

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/biswas-dev/pool/internal/store"
)

// ceremonyTTL bounds a WebAuthn exchange. The browser prompt is in front of
// the person the whole time, so this only has to outlast finding the key.
const ceremonyTTL = 5 * time.Minute

// webAuthnUser adapts an account to what go-webauthn expects.
//
// The library asks for the credentials up front so it can check that an
// assertion belongs to this account and that a registration is not a duplicate.
type webAuthnUser struct {
	user  *store.User
	creds []webauthn.Credential
}

func (u webAuthnUser) WebAuthnID() []byte {
	// A stable, opaque handle. The account id rather than the email, because a
	// user handle is stored on the authenticator and an email can change.
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(u.user.ID))
	return b[:]
}
func (u webAuthnUser) WebAuthnName() string                       { return u.user.Email }
func (u webAuthnUser) WebAuthnDisplayName() string                { return orDefault(u.user.Name, u.user.Email) }
func (u webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// webAuthn builds the relying party for this instance.
//
// The RP ID is the site's domain and the origin its exact URL, and both are
// checked on every ceremony — that pairing is what stops a passkey registered
// here from being usable by a page somewhere else.
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	u, err := url.Parse(s.Cfg.AppURL)
	if err != nil {
		return nil, err
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: "Pool",
		RPID:          u.Hostname(),
		RPOrigins:     []string{strings.TrimRight(s.Cfg.AppURL, "/")},
	})
}

// webAuthnConfigured reports whether passkeys can work here at all.
//
// They cannot over plain HTTP other than on localhost, which the browser
// enforces rather than us — so the interface should say so instead of
// offering a button that fails in the browser with no explanation.
func (s *Server) webAuthnConfigured() bool {
	u, err := url.Parse(s.Cfg.AppURL)
	if err != nil {
		return false
	}
	return u.Scheme == "https" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
}

// credentials loads a user's passkeys in the library's shape.
func (s *Server) credentials(userID int64) ([]webauthn.Credential, []store.Passkey, error) {
	stored, err := s.DB.ListPasskeys(userID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]webauthn.Credential, 0, len(stored))
	for _, p := range stored {
		id, err := base64.RawURLEncoding.DecodeString(p.CredentialID)
		if err != nil {
			log.Printf("passkey %d has an unreadable credential id: %v", p.ID, err)
			continue
		}
		key, err := base64.StdEncoding.DecodeString(p.PublicKey)
		if err != nil {
			log.Printf("passkey %d has an unreadable public key: %v", p.ID, err)
			continue
		}
		cred := webauthn.Credential{ID: id, PublicKey: key}
		cred.Authenticator.SignCount = uint32(p.SignCount)
		cred.Flags.BackupEligible = p.BackedUp
		cred.Flags.BackupState = p.BackedUp
		out = append(out, cred)
	}
	return out, stored, nil
}

// handlePasskeyRegisterBegin starts adding a passkey to the signed-in account.
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	wa, err := s.webAuthn()
	if err != nil {
		log.Printf("webauthn config: %v", err)
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}
	creds, _, err := s.credentials(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	options, session, err := wa.BeginRegistration(webAuthnUser{user: u, creds: creds},
		// Excluding what is already registered stops the same key being added
		// twice, which the authenticator reports as a plain failure otherwise.
		webauthn.WithExclusions(credentialDescriptors(creds)),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
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
	wa, err := s.webAuthn()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}
	creds, _, err := s.credentials(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Response)
	if err != nil {
		writeError(w, http.StatusBadRequest, "that passkey response could not be read")
		return
	}
	cred, err := wa.CreateCredential(webAuthnUser{user: u, creds: creds}, *session, parsed)
	if err != nil {
		// The library's message names the check that failed — origin, RP ID,
		// challenge — and that is exactly what makes this debuggable.
		log.Printf("create passkey credential: %v", err)
		writeError(w, http.StatusBadRequest, "that passkey could not be verified")
		return
	}

	saved, err := s.DB.AddPasskey(&store.Passkey{
		UserID:       u.ID,
		CredentialID: base64.RawURLEncoding.EncodeToString(cred.ID),
		PublicKey:    base64.StdEncoding.EncodeToString(cred.PublicKey),
		Attestation:  cred.AttestationType,
		Transports:   transportsOf(parsed),
		SignCount:    int64(cred.Authenticator.SignCount),
		BackedUp:     cred.Flags.BackupState,
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

// handlePasskeyLoginBegin starts a sign-in with no password.
//
// No email is asked for. A discoverable credential carries the user handle, so
// the browser can offer the right passkey and the server learns who it is from
// the assertion — which also means this endpoint reveals nothing about who has
// an account here.
func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webAuthn()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}
	options, session, err := wa.BeginDiscoverableLogin()
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
	wa, err := s.webAuthn()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkeys are not available on this instance")
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Response)
	if err != nil {
		writeError(w, http.StatusBadRequest, "that passkey response could not be read")
		return
	}

	var signedIn *store.User
	cred, err := wa.ValidateDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		// The handle says which account claims the credential; the credential
		// id says which key. Both are checked, and the library verifies the
		// signature against the stored public key afterwards.
		id, err := userIDFromHandle(userHandle)
		if err != nil {
			return nil, err
		}
		user, err := s.DB.UserByID(id)
		if err != nil {
			return nil, err
		}
		creds, _, err := s.credentials(user.ID)
		if err != nil {
			return nil, err
		}
		signedIn = user
		return webAuthnUser{user: user, creds: creds}, nil
	}, *session, parsed)
	if err != nil || signedIn == nil {
		log.Printf("validate passkey login: %v", err)
		writeError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}

	// A counter that has gone backwards means the authenticator has been
	// cloned. The library flags it; refusing is the only safe response.
	if cred.Authenticator.CloneWarning {
		log.Printf("passkey clone warning for user %d", signedIn.ID)
		writeError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}

	if stored, err := s.DB.PasskeyByCredentialID(base64.RawURLEncoding.EncodeToString(cred.ID)); err == nil {
		s.DB.TouchPasskey(stored.ID, int64(cred.Authenticator.SignCount))
	}

	// A passkey is already two factors — something you have, unlocked by
	// something you are or know — so it stands on its own without the code.
	s.startSession(w, r, signedIn)
	writeJSON(w, http.StatusOK, signedIn)
}

// handleRenamePasskey and handleDeletePasskey manage what is registered.
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

func (s *Server) storeCeremony(kind string, userID *int64, session *webauthn.SessionData) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	blob, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	return token, s.DB.CreateChallenge(token, kind, userID, string(blob), ceremonyTTL)
}

func (s *Server) takeCeremony(kind, token string, wantUser *int64) (*webauthn.SessionData, error) {
	owner, blob, err := s.DB.TakeChallenge(strings.TrimSpace(token), kind)
	if err != nil {
		return nil, err
	}
	// A registration ceremony belongs to the account that started it. Letting
	// one account finish another's would attach a passkey to the wrong user.
	if wantUser != nil && (owner == nil || *owner != *wantUser) {
		return nil, store.ErrChallengeInvalid
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(blob), &session); err != nil {
		return nil, store.ErrChallengeInvalid
	}
	return &session, nil
}

func userIDFromHandle(handle []byte) (int64, error) {
	if len(handle) != 8 {
		return 0, store.ErrNotFound
	}
	return int64(binary.BigEndian.Uint64(handle)), nil
}

func credentialDescriptors(creds []webauthn.Credential) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Descriptor())
	}
	return out
}

// transportsOf records how the authenticator can be reached — usb, nfc,
// internal — which is what lets the browser prompt for the right thing next
// time rather than offering every option.
func transportsOf(c *protocol.ParsedCredentialCreationData) string {
	if c == nil {
		return ""
	}
	out := make([]string, 0, len(c.Response.Transports))
	for _, t := range c.Response.Transports {
		out = append(out, string(t))
	}
	return strings.Join(out, ",")
}

// passkeyName bounds the label and gives an unnamed key something to be
// listed as.
func passkeyName(name string) string {
	name = trimTo(name, 60)
	if name == "" {
		return "Passkey"
	}
	return name
}
