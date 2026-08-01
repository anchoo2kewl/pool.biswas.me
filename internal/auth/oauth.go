package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Identity is the normalised result of an OAuth sign-in.
type Identity struct {
	Provider  string
	UID       string
	Email     string
	Name      string
	AvatarURL string
}

// Provider describes one OAuth provider's endpoints and scopes.
type Provider struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       string
}

// GitHub returns the GitHub provider configuration.
func GitHub(clientID, clientSecret string) *Provider {
	return &Provider{
		Name: "github", ClientID: clientID, ClientSecret: clientSecret,
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
		Scopes:   "read:user user:email",
	}
}

// Google returns the Google provider configuration.
func Google(clientID, clientSecret string) *Provider {
	return &Provider{
		Name: "google", ClientID: clientID, ClientSecret: clientSecret,
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes:   "openid email profile",
	}
}

// AuthCodeURL builds the URL the browser is redirected to.
func (p *Provider) AuthCodeURL(redirectURI, state string) string {
	v := url.Values{}
	v.Set("client_id", p.ClientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", p.Scopes)
	v.Set("state", state)
	v.Set("response_type", "code")
	if p.Name == "google" {
		// Ask for a fresh consent screen only when we have no refresh token;
		// select_account lets a user pick between multiple Google accounts.
		v.Set("prompt", "select_account")
	}
	return p.AuthURL + "?" + v.Encode()
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Exchange trades an authorization code for an access token.
func (p *Provider) Exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s token exchange: %w", p.Name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s token exchange returned %s", p.Name, resp.Status)
	}
	var tok struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("%s token response: %w", p.Name, err)
	}
	if tok.Error != "" {
		return "", fmt.Errorf("%s: %s", p.Name, cmp(tok.ErrorDescription, tok.Error))
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("%s returned no access token", p.Name)
	}
	return tok.AccessToken, nil
}

// Identity fetches the signed-in user's profile.
func (p *Provider) Identity(ctx context.Context, token string) (*Identity, error) {
	switch p.Name {
	case "github":
		return githubIdentity(ctx, token)
	case "google":
		return googleIdentity(ctx, token)
	default:
		return nil, fmt.Errorf("unknown provider %q", p.Name)
	}
}

func get(ctx context.Context, url, token string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
}

func githubIdentity(ctx context.Context, token string) (*Identity, error) {
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := get(ctx, "https://api.github.com/user", token, &u); err != nil {
		return nil, err
	}
	id := &Identity{Provider: "github", UID: strconv.FormatInt(u.ID, 10), Email: strings.ToLower(u.Email),
		Name: cmp(u.Name, u.Login), AvatarURL: u.AvatarURL}

	// A GitHub user with a private email address returns none on /user, so
	// fall back to the verified-primary address.
	if id.Email == "" {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := get(ctx, "https://api.github.com/user/emails", token, &emails); err == nil {
			for _, e := range emails {
				if e.Primary && e.Verified {
					id.Email = strings.ToLower(e.Email)
					break
				}
			}
		}
	}
	if id.Email == "" {
		return nil, fmt.Errorf("GitHub account has no verified email address; add one on GitHub and try again")
	}
	return id, nil
}

func googleIdentity(ctx context.Context, token string) (*Identity, error) {
	var u struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := get(ctx, "https://openidconnect.googleapis.com/v1/userinfo", token, &u); err != nil {
		return nil, err
	}
	if u.Email == "" {
		return nil, fmt.Errorf("Google account returned no email address")
	}
	// email_verified arrives as a bool from the OIDC endpoint but as the
	// string "true" from some older ones.
	verified := false
	switch v := u.EmailVerified.(type) {
	case bool:
		verified = v
	case string:
		verified = v == "true"
	}
	if !verified {
		return nil, fmt.Errorf("Google email address is not verified")
	}
	return &Identity{Provider: "google", UID: u.Sub, Email: strings.ToLower(u.Email),
		Name: cmp(u.Name, strings.SplitN(u.Email, "@", 2)[0]), AvatarURL: u.Picture}, nil
}

func cmp(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}
