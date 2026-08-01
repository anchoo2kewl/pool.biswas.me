// Package config reads runtime settings from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"
)

// Registration controls who may create an account.
type Registration string

const (
	// RegistrationOpen lets anyone sign up.
	RegistrationOpen Registration = "open"
	// RegistrationInvite requires a valid invite code.
	RegistrationInvite Registration = "invite"
	// RegistrationClosed allows no new accounts at all.
	RegistrationClosed Registration = "closed"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	Addr        string
	DBPath      string
	DataDir     string
	AppURL      string
	Env         string
	SessionTTLH int

	Registration Registration

	// OAuth. Each provider is enabled only when both its ID and secret are set.
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
	OAuthStateSecret   string

	// AI defaults. Users may override the key and model per account; these are
	// the fallbacks, and make the feature work out of the box.
	AIBaseURL string
	AIAPIKey  string
	AIModel   string

	// AdminEmail/AdminPassword seed the first account on an empty database.
	AdminEmail    string
	AdminPassword string

	// The public demo account. Its credentials are deliberately published on
	// the sign-in page, and its data is rebuilt on a timer so visitors can
	// change anything without spoiling it for the next person.
	DemoEnabled    bool
	DemoEmail      string
	DemoPassword   string
	DemoResetHours int

	SecureCookies bool
}

// Load reads configuration from the environment, applying defaults.
func Load() *Config {
	c := &Config{
		Addr:        env("POOL_ADDR", ":8080"),
		DBPath:      env("POOL_DB_PATH", "./data/pool.db"),
		DataDir:     env("POOL_DATA_DIR", "./data"),
		AppURL:      strings.TrimRight(env("POOL_APP_URL", "http://localhost:8080"), "/"),
		Env:         env("POOL_ENV", "development"),
		SessionTTLH: envInt("POOL_SESSION_TTL_HOURS", 24*30),

		Registration: Registration(strings.ToLower(env("POOL_REGISTRATION", string(RegistrationOpen)))),

		// Both naming conventions are accepted so the secrets already in use
		// by the blog (GH_CLIENT_ID) and TaskAI (LOGIN_GITHUB_CLIENT_ID) drop
		// in without renaming.
		GitHubClientID:     env("LOGIN_GITHUB_CLIENT_ID", env("GH_CLIENT_ID", env("GITHUB_CLIENT_ID", ""))),
		GitHubClientSecret: env("LOGIN_GITHUB_CLIENT_SECRET", env("GH_CLIENT_SECRET", env("GITHUB_CLIENT_SECRET", ""))),
		GoogleClientID:     env("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: env("GOOGLE_CLIENT_SECRET", ""),
		OAuthStateSecret:   env("OAUTH_STATE_SECRET", ""),

		AIBaseURL: env("POOL_AI_BASE_URL", "https://integrate.api.nvidia.com/v1"),
		AIAPIKey:  env("POOL_AI_API_KEY", ""),
		AIModel:   env("POOL_AI_MODEL", "deepseek-ai/deepseek-v4-pro"),

		AdminEmail:    env("POOL_ADMIN_EMAIL", ""),
		AdminPassword: env("POOL_ADMIN_PASSWORD", ""),

		DemoEnabled:    env("POOL_DEMO", "true") == "true",
		DemoEmail:      env("POOL_DEMO_EMAIL", "demo@pool.biswas.me"),
		DemoPassword:   env("POOL_DEMO_PASSWORD", "poolside"),
		DemoResetHours: envInt("POOL_DEMO_RESET_HOURS", 2),
	}

	switch c.Registration {
	case RegistrationOpen, RegistrationInvite, RegistrationClosed:
	default:
		log.Printf("config: unknown POOL_REGISTRATION %q, falling back to %q", c.Registration, RegistrationOpen)
		c.Registration = RegistrationOpen
	}

	c.SecureCookies = strings.HasPrefix(c.AppURL, "https://")

	if c.OAuthStateSecret == "" {
		// Without a stable secret, OAuth state cannot be verified across a
		// restart. A random one keeps a single process working; log it so the
		// operator knows to set one properly.
		c.OAuthStateSecret = randomHex(32)
		if c.GitHubEnabled() || c.GoogleEnabled() {
			log.Print("config: OAUTH_STATE_SECRET is unset — generated a temporary one; sign-ins started before a restart will fail")
		}
	}
	return c
}

// GitHubEnabled reports whether GitHub sign-in is configured.
func (c *Config) GitHubEnabled() bool {
	return c.GitHubClientID != "" && c.GitHubClientSecret != ""
}

// GoogleEnabled reports whether Google sign-in is configured.
func (c *Config) GoogleEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// AIEnabled reports whether a server-wide AI key is configured. Users with
// their own key get insights regardless.
func (c *Config) AIEnabled() bool { return c.AIAPIKey != "" }

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("config: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}
