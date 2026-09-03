// Package config reads runtime settings from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"

	goai "github.com/anchoo2kewl/go-ai"
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
	// AISharedKey decides whether the operator's own providers serve everyone
	// who signs up, or only the operator.
	//
	// It defaults to off. A model key is a personal thing that costs its owner
	// money per request, and a public instance where every visitor spends the
	// operator's credit is a bill waiting to happen — including the demo
	// account, which is deliberately open to the world.
	AISharedKey bool

	// AIVisionModel reads a photographed test sheet. A provider's text model
	// and its vision model are rarely the same one, and sending an image to a
	// text-only model fails at the provider rather than degrading, so the two
	// are configured separately.
	AIVisionModel string

	// AISlots and AIVisionSlots are the go-ai fallback chains: primary first,
	// then each backup. They are built from AI_n_* and AIV_n_* if those are
	// set, and otherwise from the single POOL_AI_* endpoint above.
	AISlots       []goai.Slot
	AIVisionSlots []goai.Slot

	// Mail gateway (go-email). Without one, password reset is offered nowhere
	// and says so, rather than appearing to work.
	MailURL      string
	MailKey      string
	MailFrom     string
	MailFromName string

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
		// A vision-capable default, because reading a photographed test sheet
		// is a first-class feature here rather than an extra: a text-only
		// model fails that at the provider. It is also the model 75hard runs,
		// so one NIM key behaves identically across both.
		AIModel:       env("POOL_AI_MODEL", "meta/llama-3.2-90b-vision-instruct"),
		AIVisionModel: env("POOL_AI_VISION_MODEL", ""),
		AISharedKey:   env("POOL_AI_SHARED", "false") == "true",

		MailURL:      env("POOL_MAIL_URL", ""),
		MailKey:      env("POOL_MAIL_KEY", ""),
		MailFrom:     env("POOL_MAIL_FROM", ""),
		MailFromName: env("POOL_MAIL_FROM_NAME", "Pool"),

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
	c.loadAISlots()

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

// loadAISlots resolves the provider chains.
//
// AI_n_* is go-ai's own convention and takes precedence, so an operator can
// configure a primary and two backups the same way every one of these apps
// does. POOL_AI_* stays supported as the single-endpoint shorthand this app
// shipped with, and is what the settings page writes for a user's own key.
func (c *Config) loadAISlots() {
	c.AISlots = configured(goai.SlotsFromEnv("AI"))
	if len(c.AISlots) == 0 && c.AIAPIKey != "" {
		c.AISlots = []goai.Slot{Slot(c.AIBaseURL, c.AIAPIKey, c.AIModel)}
	}

	c.AIVisionSlots = configured(goai.SlotsFromEnv("AIV"))
	if len(c.AIVisionSlots) == 0 && c.AIVisionModel != "" && c.AIAPIKey != "" {
		c.AIVisionSlots = []goai.Slot{Slot(c.AIBaseURL, c.AIAPIKey, c.AIVisionModel)}
	}
	// No vision slots is not a failure: the service falls back to the text
	// chain, which on NVIDIA NIM and OpenRouter is usually multimodal anyway.
}

// configured drops slots that name a provider but carry nothing to call it
// with — an AI_1_PROVIDER whose AI_1_API_KEY secret is not populated yet.
//
// The filter is what makes the fallback to POOL_AI_* mean "no chain is
// configured" rather than "a chain was declared". Without it, a deploy
// template that always writes AI_1_PROVIDER would switch the AI features off
// entirely on any environment where the key is missing, while a perfectly good
// POOL_AI_API_KEY sat unused beside it.
func configured(slots []goai.Slot) []goai.Slot {
	out := slots[:0]
	for _, s := range slots {
		if s.Configured() {
			out = append(out, s)
		}
	}
	return out
}

// Slot describes one OpenAI-compatible endpoint to go-ai.
//
// The provider name is recovered from the base URL where it is one go-ai
// knows, so the fallback log says "nvidia" rather than "custom". An unknown
// endpoint keeps its explicit base URL, which is all go-ai needs.
func Slot(baseURL, apiKey, model string) goai.Slot {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	name := "custom"
	for _, known := range goai.KnownProviders() {
		if url := goai.BaseURLFor(known); url != "" && strings.EqualFold(url, baseURL) {
			name = known
			break
		}
	}
	return goai.Slot{Provider: name, BaseURL: baseURL, APIKey: apiKey, Model: model}
}

// MailEnabled reports whether a mail gateway is configured, which is what
// decides whether the sign-in page offers a password reset at all.
func (c *Config) MailEnabled() bool {
	return c.MailURL != "" && c.MailKey != "" && c.MailFrom != ""
}

// GitHubEnabled reports whether GitHub sign-in is configured.
func (c *Config) GitHubEnabled() bool {
	return c.GitHubClientID != "" && c.GitHubClientSecret != ""
}

// GoogleEnabled reports whether Google sign-in is configured.
func (c *Config) GoogleEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// AIEnabled reports whether any server-wide AI provider is configured. Users
// with their own key get insights regardless.
func (c *Config) AIEnabled() bool { return len(c.AISlots) > 0 }

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
