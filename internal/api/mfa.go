package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	gologin "github.com/anchoo2kewl/go-login"

	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/store"
)

// mfaChallengeTTL bounds the gap between a correct password and the second
// factor. Long enough to fetch a phone from another room, short enough that a
// half-finished login left on a shared machine is not a standing invitation.
const mfaChallengeTTL = 5 * time.Minute

// handleMFAStatus describes the account's second factor and passkeys, which is
// what the settings page renders from.
func (s *Server) handleMFAStatus(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)

	secret, confirmed, err := s.DB.TOTPSecret(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	left, err := s.DB.RecoveryCodesLeft(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	keys, err := s.DB.ListPasskeys(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totp": map[string]any{
			"enabled": confirmed,
			// A secret issued but never proved: the setup was abandoned
			// halfway, and the interface should offer to finish or discard it.
			"pending":             secret != "" && !confirmed,
			"recovery_codes_left": left,
			"recovery_code_total": gologin.RecoveryCodeCount,
		},
		"passkeys":         keys,
		"passkeys_enabled": s.webAuthnConfigured(),
	})
}

// handleTOTPBegin issues a secret and the QR an authenticator app scans.
//
// The secret is stored unconfirmed. Turning the factor on before a code has
// come back would let a mistyped setup lock somebody out of their own account.
func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)

	if _, confirmed, err := s.DB.TOTPSecret(u.ID); err != nil {
		writeStoreError(w, err)
		return
	} else if confirmed {
		writeError(w, http.StatusConflict, "two-factor authentication is already on for this account")
		return
	}

	secret, err := gologin.NewTOTPSecret()
	if err != nil {
		log.Printf("generate totp secret: %v", err)
		writeError(w, http.StatusInternalServerError, "could not start setup")
		return
	}
	if err := s.DB.SetTOTPSecret(u.ID, secret); err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"secret":    gologin.FormatTOTPSecret(secret),
		"uri":       gologin.TOTPURI(s.issuer(), u.Email, secret),
		"qr_url":    "/api/me/mfa/totp/qr",
		"digits":    gologin.TOTPDigits,
		"period_s":  int(gologin.TOTPStep.Seconds()),
		"next_step": "Scan the code, then enter the six digits your app shows to switch it on.",
	})
}

// handleTOTPQR renders the enrolment QR.
//
// Served as an image from an authenticated endpoint rather than embedded in
// the page, so the secret does not end up in the page source, the browser
// history, or a screenshot of the URL bar.
func (s *Server) handleTOTPQR(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	secret, confirmed, err := s.DB.TOTPSecret(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if secret == "" || confirmed {
		// Nothing to enrol: either setup has not started, or it is finished
		// and the secret is no longer anybody's business.
		writeError(w, http.StatusNotFound, "no enrolment in progress")
		return
	}

	png, err := qrcode.Encode(gologin.TOTPURI(s.issuer(), u.Email, secret), qrcode.Medium, 320)
	if err != nil {
		log.Printf("render totp qr: %v", err)
		writeError(w, http.StatusInternalServerError, "could not draw the code")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

// handleTOTPConfirm proves the secret arrived and switches the factor on,
// handing back the recovery codes.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, confirmed, err := s.DB.TOTPSecret(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if secret == "" {
		writeError(w, http.StatusBadRequest, "start the setup first")
		return
	}
	if confirmed {
		writeError(w, http.StatusConflict, "two-factor authentication is already on")
		return
	}
	if !gologin.VerifyTOTP(secret, req.Code, time.Now()) {
		writeError(w, http.StatusBadRequest, "that code is not right — check your phone's clock and try the current one")
		return
	}

	codes, hashes, err := gologin.NewRecoveryCodes()
	if err != nil {
		log.Printf("generate recovery codes: %v", err)
		writeError(w, http.StatusInternalServerError, "could not generate recovery codes")
		return
	}
	if err := s.DB.ReplaceRecoveryCodes(u.ID, hashes); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.DB.ConfirmTOTP(u.ID); err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		// Shown once. They are the way back in when the phone is gone, so the
		// interface has to insist they are written down now.
		"recovery_codes": codes,
		"message":        "Two-factor authentication is on. Save these recovery codes somewhere safe — each works once, and this is the only time they are shown.",
	})
}

// handleTOTPDisable turns the factor off, on proof of the password.
//
// Requiring the password matters: without it, anybody who found an unlocked
// browser could quietly remove the protection and leave the account looking
// untouched.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.passwordMatches(u, req.Password) {
		writeError(w, http.StatusUnauthorized, "that password is not right")
		return
	}
	if err := s.DB.DisableTOTP(u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Two-factor authentication is off, and the recovery codes that went with it no longer work.",
	})
}

// handleRecoveryCodesRegenerate issues a fresh set, retiring the old.
func (s *Server) handleRecoveryCodesRegenerate(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.passwordMatches(u, req.Password) {
		writeError(w, http.StatusUnauthorized, "that password is not right")
		return
	}
	if _, confirmed, err := s.DB.TOTPSecret(u.ID); err != nil {
		writeStoreError(w, err)
		return
	} else if !confirmed {
		writeError(w, http.StatusBadRequest, "turn two-factor authentication on first")
		return
	}

	codes, hashes, err := gologin.NewRecoveryCodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate recovery codes")
		return
	}
	if err := s.DB.ReplaceRecoveryCodes(u.ID, hashes); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recovery_codes": codes,
		"message":        "These replace your previous codes, which no longer work.",
	})
}

// ── The second step of a sign-in ─────────────────────────────────────────

// handleMFAChallenge completes a login that stopped at the second factor.
func (s *Server) handleMFAChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	userID, _, err := s.DB.TakeChallenge(strings.TrimSpace(req.Challenge), store.ChallengeMFALogin)
	if err != nil || userID == nil {
		writeError(w, http.StatusUnauthorized, store.ErrChallengeInvalid.Error())
		return
	}
	user, err := s.DB.UserByID(*userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, store.ErrChallengeInvalid.Error())
		return
	}
	secret, confirmed, err := s.DB.TOTPSecret(user.ID)
	if err != nil || !confirmed {
		writeError(w, http.StatusUnauthorized, store.ErrChallengeInvalid.Error())
		return
	}

	code := strings.TrimSpace(req.Code)
	switch {
	case gologin.VerifyTOTP(secret, code, time.Now()):
		// A code from the authenticator.
	default:
		// Or one of the recovery codes, spent as it is used.
		used, err := s.DB.UseRecoveryCode(user.ID, gologin.HashRecoveryCode(code))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if !used {
			writeError(w, http.StatusUnauthorized, "that code is not right")
			return
		}
		log.Printf("user %d signed in with a recovery code", user.ID)
	}

	s.startSession(w, r, user)
	writeJSON(w, http.StatusOK, user)
}

// beginMFAChallenge parks a verified password and asks for the second factor.
func (s *Server) beginMFAChallenge(w http.ResponseWriter, user *store.User) {
	token, err := randomToken()
	if err != nil {
		log.Printf("generate mfa challenge: %v", err)
		writeError(w, http.StatusInternalServerError, "could not start the second step")
		return
	}
	if err := s.DB.CreateChallenge(token, store.ChallengeMFALogin, &user.ID, "", mfaChallengeTTL); err != nil {
		writeStoreError(w, err)
		return
	}
	left, _ := s.DB.RecoveryCodesLeft(user.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"mfa_required":        true,
		"challenge":           token,
		"recovery_codes_left": left,
		"message":             "Enter the six-digit code from your authenticator app, or one of your recovery codes.",
	})
}

// passwordMatches checks a password for an action that needs re-proving it.
func (s *Server) passwordMatches(u *store.User, password string) bool {
	_, hash, err := s.DB.PasswordHash(u.Email)
	if err != nil || hash == "" {
		return false
	}
	return auth.VerifyPassword(password, hash) == nil
}

// issuer is the name an authenticator app files the account under.
func (s *Server) issuer() string {
	host := strings.TrimPrefix(strings.TrimPrefix(s.Cfg.AppURL, "https://"), "http://")
	if i := strings.IndexAny(host, ":/"); i > 0 {
		host = host[:i]
	}
	if host == "" {
		return "Pool"
	}
	return host
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var _ = errors.Is
