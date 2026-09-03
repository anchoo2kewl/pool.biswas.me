package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Balance is what is left to spend on a provider's key.
//
// Not every provider has the notion. NVIDIA's build endpoint is free to use
// with a personal key, and OpenAI does not expose a balance to an API key at
// all — so Supported says whether a number could ever be had, which is a
// different thing from the lookup failing.
type Balance struct {
	Provider  string `json:"provider"`
	Supported bool   `json:"supported"`
	// Note explains an unsupported provider in the words the interface shows.
	Note string `json:"note,omitempty"`
	// Available is false when the provider says the account cannot spend.
	Available bool    `json:"available"`
	Currency  string  `json:"currency,omitempty"`
	Amount    float64 `json:"amount"`
	// Display is the amount as the provider itself formats it, so a currency
	// this app does not know is still shown correctly.
	Display string `json:"display,omitempty"`
	// Error is set when the provider was asked and would not answer — a
	// revoked key, most often, which is worth showing rather than hiding.
	Error string `json:"error,omitempty"`
}

// FreeProviders are the ones with no balance to report because using them
// costs nothing.
var freeProviders = map[string]string{
	"nvidia": "NVIDIA's build endpoint is free with a personal key — there is no balance to run down.",
	"ollama": "Ollama runs on your own machine, so there is nothing to bill.",
}

// LookupBalance asks a provider what is left on a key.
//
// It is deliberately narrow: DeepSeek publishes a balance endpoint, and the
// rest either do not or do not expose one to an API key. Guessing at an
// endpoint per vendor would be a maintenance burden that pays for itself only
// for the one people actually top up.
func LookupBalance(ctx context.Context, provider, apiKey string) Balance {
	provider = strings.ToLower(strings.TrimSpace(provider))
	b := Balance{Provider: provider}

	if note, free := freeProviders[provider]; free {
		b.Supported, b.Available, b.Note = false, true, note
		return b
	}
	if provider != "deepseek" {
		b.Note = "This provider does not publish a balance for an API key."
		return b
	}
	b.Supported = true
	if strings.TrimSpace(apiKey) == "" {
		b.Error = "no key configured"
		return b
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.deepseek.com/user/balance", nil)
	if err != nil {
		b.Error = err.Error()
		return b
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Error = "could not reach DeepSeek"
		return b
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		b.Error = "DeepSeek rejected this key"
		return b
	}
	if resp.StatusCode != http.StatusOK {
		b.Error = fmt.Sprintf("DeepSeek returned %s", resp.Status)
		return b
	}

	var out struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency       string `json:"currency"`
			TotalBalance   string `json:"total_balance"`
			GrantedBalance string `json:"granted_balance"`
			ToppedUp       string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		b.Error = "could not read DeepSeek's answer"
		return b
	}

	b.Available = out.IsAvailable
	if len(out.BalanceInfos) > 0 {
		info := out.BalanceInfos[0]
		b.Currency = info.Currency
		// The amounts arrive as strings, because they are money.
		if v, err := strconv.ParseFloat(info.TotalBalance, 64); err == nil {
			b.Amount = v
		}
		b.Display = info.TotalBalance + " " + info.Currency
	}
	return b
}
