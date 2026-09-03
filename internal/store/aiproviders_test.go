package store

import "testing"

func TestAIProviderSlots(t *testing.T) {
	db := open(t)
	u, err := db.CreateUser("slots@example.com", "Slots", "hash", "member")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	p, err := db.SetAIProvider(&AIProvider{UserID: u.ID, Kind: AIKindText, Slot: 1,
		Provider: "DeepSeek", Model: "deepseek-v4-flash", APIKey: "sk-1234567890abcd"})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if p.Provider != "deepseek" {
		t.Errorf("provider = %q, want it normalised to lower case", p.Provider)
	}
	if !p.HasKey || p.KeyHint != "sk-…abcd" {
		t.Errorf("HasKey=%v hint=%q, want the vendor marker and last four", p.HasKey, p.KeyHint)
	}

	// Editing a slot without resending the secret must keep it. Otherwise the
	// interface has to hold the key in a form field to change a model.
	p, err = db.SetAIProvider(&AIProvider{UserID: u.ID, Kind: AIKindText, Slot: 1,
		Provider: "deepseek", Model: "deepseek-v4-pro"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !p.HasKey {
		t.Error("the stored key was dropped when the model was changed")
	}
	if p.Model != "deepseek-v4-pro" {
		t.Errorf("model = %q, want the update applied", p.Model)
	}

	// The two chains are independent.
	if _, err := db.SetAIProvider(&AIProvider{UserID: u.ID, Kind: AIKindVision, Slot: 1,
		Provider: "nvidia", Model: "meta/llama-3.2-90b-vision-instruct", APIKey: "nvapi-abcdefghij"}); err != nil {
		t.Fatalf("set vision: %v", err)
	}
	list, err := db.ListAIProviders(u.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d slots, want 2", len(list))
	}
	for _, got := range list {
		if got.APIKey != "" {
			// The struct is serialised straight to the client.
			t.Error("the key travels on the listed struct")
		}
	}

	if _, err := db.SetAIProvider(&AIProvider{UserID: u.ID, Kind: "audio", Slot: 1, Provider: "x"}); err == nil {
		t.Error("an unknown chain was accepted")
	}
	if _, err := db.SetAIProvider(&AIProvider{UserID: u.ID, Kind: AIKindText, Slot: 9, Provider: "x"}); err == nil {
		t.Error("a slot beyond the limit was accepted")
	}

	if err := db.DeleteAIProvider(u.ID, AIKindText, 1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.DeleteAIProvider(u.ID, AIKindText, 1); err == nil {
		t.Error("deleting a slot that is already gone reported success")
	}
}
