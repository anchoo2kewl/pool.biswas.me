package config

import "testing"

// A deploy template writes AI_1_PROVIDER unconditionally, so an environment
// where the key secret is not populated yet must fall back to POOL_AI_* rather
// than reading "a chain was declared" and switching the AI features off.
func TestSlotsFallBackWhenTheChainHasNoKey(t *testing.T) {
	t.Setenv("AI_1_PROVIDER", "nvidia")
	t.Setenv("AI_1_MODEL", "meta/llama-3.2-90b-vision-instruct")
	t.Setenv("AI_1_API_KEY", "")
	t.Setenv("POOL_AI_API_KEY", "fallback-key")

	c := Load()
	if len(c.AISlots) != 1 {
		t.Fatalf("AISlots = %d, want the single POOL_AI_* slot", len(c.AISlots))
	}
	if c.AISlots[0].APIKey != "fallback-key" {
		t.Errorf("APIKey = %q, want the POOL_AI_API_KEY fallback", c.AISlots[0].APIKey)
	}
	if !c.AIEnabled() {
		t.Error("AIEnabled() = false with a usable POOL_AI_API_KEY configured")
	}
}

func TestChainWinsOverTheSingleEndpoint(t *testing.T) {
	t.Setenv("AI_1_PROVIDER", "nvidia")
	t.Setenv("AI_1_MODEL", "meta/llama-3.2-90b-vision-instruct")
	t.Setenv("AI_1_API_KEY", "chain-key-1")
	t.Setenv("AI_2_PROVIDER", "deepseek")
	t.Setenv("AI_2_MODEL", "deepseek-v4-flash")
	t.Setenv("AI_2_API_KEY", "chain-key-2")
	t.Setenv("POOL_AI_API_KEY", "fallback-key")

	c := Load()
	if len(c.AISlots) != 2 {
		t.Fatalf("AISlots = %d, want both chain slots", len(c.AISlots))
	}
	if c.AISlots[0].Provider != "nvidia" || c.AISlots[1].Provider != "deepseek" {
		t.Errorf("chain = %v, want nvidia then deepseek in that order",
			[]string{c.AISlots[0].Provider, c.AISlots[1].Provider})
	}
}

// A half-configured chain keeps the rungs that work rather than failing whole.
func TestPartiallyConfiguredChainKeepsWhatWorks(t *testing.T) {
	t.Setenv("AI_1_PROVIDER", "nvidia")
	t.Setenv("AI_1_MODEL", "meta/llama-3.2-90b-vision-instruct")
	t.Setenv("AI_1_API_KEY", "chain-key-1")
	t.Setenv("AI_2_PROVIDER", "deepseek")
	t.Setenv("AI_2_MODEL", "deepseek-v4-flash")
	t.Setenv("AI_2_API_KEY", "")

	c := Load()
	if len(c.AISlots) != 1 || c.AISlots[0].Provider != "nvidia" {
		t.Errorf("AISlots = %v, want just the nvidia slot", c.AISlots)
	}
}

// The provider name is recovered from the base URL so a fallback log names the
// vendor rather than "custom".
func TestSlotNamesKnownProvidersFromTheirURL(t *testing.T) {
	if got := Slot("https://integrate.api.nvidia.com/v1", "k", "m").Provider; got != "nvidia" {
		t.Errorf("provider = %q, want nvidia", got)
	}
	if got := Slot("https://llm.example.internal/v1", "k", "m").Provider; got != "custom" {
		t.Errorf("provider = %q, want custom for an endpoint go-ai does not know", got)
	}
	// An unknown endpoint has to keep its base URL, which is all go-ai needs
	// to call it.
	if got := Slot("https://llm.example.internal/v1", "k", "m").BaseURL; got != "https://llm.example.internal/v1" {
		t.Errorf("base URL = %q, want it preserved", got)
	}
}

// A model key costs its owner money per request, so the operator's providers
// must not serve every visitor unless the operator has said so.
func TestSharedKeyIsOffByDefault(t *testing.T) {
	t.Setenv("POOL_AI_API_KEY", "operator-key")
	if Load().AISharedKey {
		t.Error("AISharedKey defaults to true — every signup would spend the operator's credit")
	}
	t.Setenv("POOL_AI_SHARED", "true")
	if !Load().AISharedKey {
		t.Error("POOL_AI_SHARED=true did not enable sharing")
	}
	t.Setenv("POOL_AI_SHARED", "yes")
	if Load().AISharedKey {
		t.Error("only an explicit \"true\" should share the operator's key")
	}
}
