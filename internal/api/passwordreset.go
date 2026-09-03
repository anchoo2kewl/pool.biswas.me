package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/store"
)

// resetTTL is how long a reset link lasts. Long enough to find the mail on
// another device, short enough that one left in an inbox is not a standing key
// to the account.
const resetTTL = time.Hour

// resetCooldown is the shortest gap between links for one account. It bounds
// how much mail a stranger can aim at somebody's inbox by typing their address
// repeatedly, without telling the stranger anything.
const resetCooldown = 60 * time.Second

// handleForgotPassword starts a reset.
//
// The answer is the same whether or not the address has an account. Anything
// else turns this endpoint into a way to ask which email addresses are
// registered, which is not a question a stranger should be able to put to it.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// One reply, decided before anything is looked up.
	const answer = "If that address has an account, a reset link is on its way. It is good for one hour."
	respond := func() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": answer})
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		respond()
		return
	}
	if !s.Mail.Configured() {
		// Worth one loud line in the log: the feature is offered on the sign-in
		// page and silently does nothing without a gateway.
		log.Printf("password reset requested for %s but no mail gateway is configured", email)
		respond()
		return
	}

	user, err := s.DB.UserByEmail(email)
	if err != nil {
		respond()
		return
	}
	if at, ok := s.DB.LastPasswordResetAt(user.ID); ok && time.Since(at) < resetCooldown {
		respond()
		return
	}

	token, err := newResetToken()
	if err != nil {
		log.Printf("generate reset token: %v", err)
		respond()
		return
	}
	expires, err := s.DB.CreatePasswordReset(user.ID, token, resetTTL, clientIP(r))
	if err != nil {
		log.Printf("record password reset: %v", err)
		respond()
		return
	}

	// Sent on its own goroutine with its own deadline: a slow gateway must not
	// hold the request open, and the reply is the same either way.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		link := fmt.Sprintf("%s/reset?token=%s", s.Cfg.AppURL, token)
		if err := s.Mail.Send(ctx, user.Email, "Reset your pool password",
			resetEmail(user.Name, link, expires)); err != nil {
			log.Printf("send reset mail to %s: %v", user.Email, err)
		}
	}()

	respond()
}

// resetEmail is the message body. Plain text, short, and it says what to do if
// the request was not theirs — which is the line that matters most, because
// the person reading it may be the one being attacked.
func resetEmail(name, link string, expires time.Time) string {
	greeting := "Hello,"
	if n := strings.TrimSpace(name); n != "" {
		greeting = "Hello " + n + ","
	}
	return fmt.Sprintf(`%s

Someone asked to reset the password on your pool.biswas.me account. Open this
link to choose a new one:

%s

The link works once and expires at %s UTC.

If this was not you, nothing has changed and you can ignore this message. Your
current password still works, and nobody can see it — including us.
`, greeting, link, expires.Format("15:04 on 2 January 2006"))
}

// handleResetPassword completes a reset.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, store.ErrResetInvalid.Error())
		return
	}

	// Hashing before the token is checked, so the work is the same for a real
	// token and a guessed one.
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	userID, err := s.DB.ConsumePasswordReset(strings.TrimSpace(req.Token), hash)
	if err != nil {
		if errors.Is(err, store.ErrResetInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	log.Printf("password reset completed for user %d", userID)

	// Every session was cleared with the password, this browser's included, so
	// there is nothing to sign the caller into. Signing in with the new
	// password is the point at which they prove they have it.
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Your password has been changed and every signed-in session was ended. Sign in with the new one.",
	})
}

func newResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// clientIP records who asked, for looking at later. It trusts the proxy header
// only for the first hop, which is all this deployment has.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("CF-Connecting-IP"); fwd != "" {
		return fwd
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
