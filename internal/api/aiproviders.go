package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	goai "github.com/anchoo2kewl/go-ai"

	"github.com/biswas-dev/pool/internal/ai"
	"github.com/biswas-dev/pool/internal/store"
)

// handleListAIProviders returns the caller's own model chains.
//
// Keys never come back — only whether a slot has one and the last four
// characters, which is how the vendors themselves print a key you already own.
func (s *Server) handleListAIProviders(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	list, err := s.DB.ListAIProviders(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	text, vision := []store.AIProvider{}, []store.AIProvider{}
	for _, p := range list {
		if p.Kind == store.AIKindVision {
			vision = append(vision, p)
		} else {
			text = append(text, p)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"text":   text,
		"vision": vision,
		// Whether this account is running on its own providers or the
		// operator's, which is the thing somebody actually wants to know.
		"using_own_keys": len(list) > 0,
		"server_chain":   s.AI.Providers(),
		"max_slots":      store.MaxAISlots,
		"providers":      knownProviders(),
	})
}

// providerChoice is one option the settings page offers.
type providerChoice struct {
	Name string `json:"name"`
	// Label is how it is written for a person.
	Label string `json:"label"`
	// BaseURL is filled in for a provider go-ai already knows, so the field
	// can stay hidden unless somebody is pointing at something unusual.
	BaseURL string `json:"base_url,omitempty"`
	// Suggested is a model known to work here, so a new account does not have
	// to go and look one up before anything happens.
	Suggested string `json:"suggested_model,omitempty"`
	// Vision says whether it can read a photographed test sheet.
	Vision bool `json:"vision"`
	// Free marks a provider that costs nothing to use.
	Free bool `json:"free,omitempty"`
	// SignUp is where to get a key.
	SignUp string `json:"sign_up,omitempty"`
}

// knownProviders is what the settings page lists. The suggestions are the
// models this app has actually been run against, not a catalogue.
func knownProviders() []providerChoice {
	return []providerChoice{
		{Name: "deepseek", Label: "DeepSeek", BaseURL: goai.BaseURLFor("deepseek"),
			Suggested: "deepseek-v4-flash", Vision: true,
			SignUp: "https://platform.deepseek.com"},
		{Name: "nvidia", Label: "NVIDIA NIM", BaseURL: goai.BaseURLFor("nvidia"),
			Suggested: "meta/llama-3.2-90b-vision-instruct", Vision: true, Free: true,
			SignUp: "https://build.nvidia.com"},
		{Name: "anthropic", Label: "Anthropic (Claude)",
			Suggested: "claude-sonnet-5", Vision: true,
			SignUp: "https://console.anthropic.com"},
		{Name: "openai", Label: "OpenAI", BaseURL: goai.BaseURLFor("openai"),
			Suggested: "gpt-5.2", Vision: true, SignUp: "https://platform.openai.com"},
		{Name: "openrouter", Label: "OpenRouter", BaseURL: goai.BaseURLFor("openrouter"),
			Suggested: "meta-llama/llama-3.2-90b-vision-instruct", Vision: true,
			SignUp: "https://openrouter.ai"},
		{Name: "groq", Label: "Groq", BaseURL: goai.BaseURLFor("groq"),
			Suggested: "llama-3.3-70b-versatile", SignUp: "https://console.groq.com"},
		{Name: "ollama", Label: "Ollama (local)", BaseURL: goai.BaseURLFor("ollama"),
			Suggested: "llama3.2-vision", Vision: true, Free: true},
	}
}

// handleSetAIProvider writes one rung of one chain.
func (s *Server) handleSetAIProvider(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		Kind     string `json:"kind"`
		Slot     int64  `json:"slot"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url"`
		// APIKey is optional on an existing slot: left empty, the stored key
		// is kept, so a model can be changed without handing the secret back
		// and forth through the browser.
		APIKey string `json:"api_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Kind == "" {
		req.Kind = store.AIKindText
	}
	if !store.ValidAIKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "kind must be text or vision")
		return
	}

	saved, err := s.DB.SetAIProvider(&store.AIProvider{
		UserID: u.ID, Kind: req.Kind, Slot: req.Slot,
		Provider: req.Provider, Model: req.Model,
		BaseURL: strings.TrimSpace(req.BaseURL), APIKey: strings.TrimSpace(req.APIKey),
	})
	if err != nil {
		if strings.Contains(err.Error(), "slot must be") || strings.Contains(err.Error(), "required") ||
			strings.Contains(err.Error(), "unknown chain") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	saved.APIKey = ""
	writeJSON(w, http.StatusOK, saved)
}

// handleDeleteAIProvider removes one rung.
func (s *Server) handleDeleteAIProvider(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	kind := r.PathValue("kind")
	if !store.ValidAIKind(kind) {
		writeError(w, http.StatusBadRequest, "kind must be text or vision")
		return
	}
	slot, err := strconv.ParseInt(r.PathValue("slot"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid slot")
		return
	}
	if err := s.DB.DeleteAIProvider(u.ID, kind, slot); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleAIBalance reports what is left on each of the caller's own keys.
//
// Only a provider that publishes a balance for an API key can answer, which
// today means DeepSeek; the free ones say so instead of showing nothing, since
// "no balance" and "costs nothing" look identical in an empty field.
func (s *Server) handleAIBalance(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	slots, err := s.DB.AIChainSlots(u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()

	// One lookup per distinct key, not per slot: the same key is normally in
	// both the text and the vision chain, and asking twice would show the same
	// number twice and spend two round trips doing it.
	seen := map[string]bool{}
	out := []map[string]any{}
	for _, p := range slots {
		if p.APIKey == "" {
			continue
		}
		fingerprint := p.Provider + "\x00" + p.APIKey
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true

		balance := ai.LookupBalance(ctx, p.Provider, p.APIKey)
		out = append(out, map[string]any{
			"kind": p.Kind, "slot": p.Slot, "key_hint": p.KeyHint, "balance": balance,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}
