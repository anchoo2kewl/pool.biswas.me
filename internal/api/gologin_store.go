package api

import (
	"context"
	"errors"

	gologin "github.com/anchoo2kewl/go-login"

	"github.com/biswas-dev/pool/internal/config"
	"github.com/biswas-dev/pool/internal/store"
)

// gologinStore adapts pool's store to go-login's UserStore.
//
// It exists so the OAuth flow itself — state signing, the provider exchange,
// the profile fetch — comes from the same library every one of these apps
// uses, while account resolution stays here where pool's own rules live: an
// existing link wins, then a matching email so a password account can adopt a
// Google identity, then a new account if registration is open.
type gologinStore struct{ s *Server }

func (g gologinStore) FindUserByProviderID(_ context.Context, provider, providerUserID string) (*gologin.User, error) {
	u, err := g.s.DB.UserByIdentity(provider, providerUserID)
	if errors.Is(err, store.ErrNotFound) {
		// go-login reads (nil, nil) as "no such user", which is not an error.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gologin.User{ID: u.ID, Email: u.Email}, nil
}

func (g gologinStore) FindUserByEmail(_ context.Context, email string) (*gologin.User, error) {
	u, err := g.s.DB.UserByEmail(email)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gologin.User{ID: u.ID, Email: u.Email}, nil
}

func (g gologinStore) GetUserAuthProvider(_ context.Context, userID int64) (string, error) {
	providers, err := g.s.DB.ListIdentities(userID)
	if err != nil {
		return "", err
	}
	if len(providers) > 0 {
		return providers[0], nil
	}
	return "password", nil
}

func (g gologinStore) CreateOAuthUser(_ context.Context, info gologin.ProviderUserInfo, provider, _ string) (*gologin.User, error) {
	if g.s.Cfg.Registration == config.RegistrationClosed {
		return nil, errors.New("registration is closed")
	}

	// The first account to exist owns the instance.
	role := "member"
	if n, err := g.s.DB.CountUsers(); err == nil && n == 0 {
		role = "admin"
	}

	name := info.Name
	if name == "" {
		name = info.Email
	}

	// OAuth accounts have no password. The column is not nullable, so an empty
	// string is stored — a value no password hash can ever equal.
	u, err := g.s.DB.CreateUser(info.Email, name, "", role)
	if err != nil {
		return nil, err
	}
	if err := g.s.DB.LinkIdentity(provider, info.ProviderUserID, u.ID, info.Email, info.AvatarURL); err != nil {
		return nil, err
	}
	return &gologin.User{ID: u.ID, Email: u.Email}, nil
}

// LinkOAuthProvider attaches a provider to an account that already exists,
// which is how somebody who signed up with a password later signs in with
// Google. go-login does not pass the profile here, so the email and avatar are
// filled from the account itself; the next sign-in refreshes them.
func (g gologinStore) LinkOAuthProvider(_ context.Context, userID int64, provider, providerUserID string) (*gologin.User, error) {
	u, err := g.s.DB.UserByID(userID)
	if err != nil {
		return nil, err
	}
	if err := g.s.DB.LinkIdentity(provider, providerUserID, u.ID, u.Email, ""); err != nil {
		return nil, err
	}
	return &gologin.User{ID: u.ID, Email: u.Email}, nil
}

func (g gologinStore) ValidateInviteCode(_ context.Context, code string) (*gologin.InviteInfo, error) {
	// Pool gates OAuth sign-up on the registration mode rather than per-invite
	// codes, so any code is accepted here and CreateOAuthUser does the check.
	return &gologin.InviteInfo{Code: code}, nil
}
