package store

import (
	"errors"
	"fmt"
	"strings"
)

// AIProvider is one rung of a user's own model chain.
//
// The key is never serialised. What the interface needs to show is whether a
// slot has one and enough of it to recognise, which is what HasKey and
// KeyHint carry.
type AIProvider struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	Kind      string `json:"kind"`
	Slot      int64  `json:"slot"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"-"`
	HasKey    bool   `json:"has_key"`
	KeyHint   string `json:"key_hint,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AIKindText and AIKindVision are the two chains an account can configure.
const (
	AIKindText   = "text"
	AIKindVision = "vision"
)

// MaxAISlots is how many rungs a chain may have. Three is the depth go-ai's
// own convention documents, and more backups than that is a sign the primary
// is the thing that needs fixing.
const MaxAISlots = 3

// ValidAIKind reports whether kind is one of the two chains.
func ValidAIKind(kind string) bool {
	return kind == AIKindText || kind == AIKindVision
}

const aiProviderCols = `id, user_id, kind, slot, provider, model, base_url, api_key, created_at, updated_at`

func scanAIProvider(row interface{ Scan(...any) error }) (*AIProvider, error) {
	var p AIProvider
	if err := row.Scan(&p.ID, &p.UserID, &p.Kind, &p.Slot, &p.Provider, &p.Model,
		&p.BaseURL, &p.APIKey, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.HasKey = p.APIKey != ""
	p.KeyHint = keyHint(p.APIKey)
	return &p, nil
}

// keyHint is the most of a key that can be shown without helping anyone use
// it: the vendor's own marker, and the last four characters, which is how the
// vendors themselves print it back.
func keyHint(key string) string {
	if len(key) < 10 {
		return ""
	}
	marker := ""
	if i := strings.Index(key, "-"); i > 0 && i < 8 {
		marker = key[:i+1]
	}
	return marker + "…" + key[len(key)-4:]
}

// ListAIProviders returns a user's configured slots for display, primary
// first, with the keys stripped.
//
// The redaction is here rather than left to the JSON tag because this is what
// handlers hand to the client: a struct tag is one careless edit away from
// leaking every key on the instance, and a nil field cannot leak at all.
// Building a chain uses AIChainSlots instead, which keeps them.
func (db *DB) ListAIProviders(userID int64) ([]AIProvider, error) {
	out, err := db.AIChainSlots(userID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].APIKey = ""
	}
	return out, nil
}

// AIChainSlots returns a user's slots with their keys, for building a chain.
func (db *DB) AIChainSlots(userID int64) ([]AIProvider, error) {
	rows, err := db.Query(`SELECT `+aiProviderCols+`
		  FROM ai_providers WHERE user_id = ? ORDER BY kind, slot`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AIProvider{}
	for rows.Next() {
		p, err := scanAIProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SetAIProvider writes one slot, replacing whatever was there.
//
// An empty key on an existing slot keeps the stored one, so the interface can
// edit a model or an endpoint without asking for the secret again — and
// without having to hold it in a form field to send it back.
func (db *DB) SetAIProvider(p *AIProvider) (*AIProvider, error) {
	if !ValidAIKind(p.Kind) {
		return nil, fmt.Errorf("unknown chain %q", p.Kind)
	}
	if p.Slot < 1 || p.Slot > MaxAISlots {
		return nil, fmt.Errorf("slot must be between 1 and %d", MaxAISlots)
	}
	if strings.TrimSpace(p.Provider) == "" {
		return nil, errors.New("a provider is required")
	}

	if p.APIKey == "" {
		var existing string
		db.QueryRow(`SELECT api_key FROM ai_providers WHERE user_id = ? AND kind = ? AND slot = ?`,
			p.UserID, p.Kind, p.Slot).Scan(&existing)
		p.APIKey = existing
	}

	now := Now()
	_, err := db.Exec(`
		INSERT INTO ai_providers (user_id, kind, slot, provider, model, base_url, api_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, kind, slot) DO UPDATE SET
		  provider = excluded.provider, model = excluded.model,
		  base_url = excluded.base_url, api_key = excluded.api_key,
		  updated_at = excluded.updated_at`,
		p.UserID, p.Kind, p.Slot, strings.ToLower(strings.TrimSpace(p.Provider)),
		strings.TrimSpace(p.Model), strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"),
		p.APIKey, now, now)
	if err != nil {
		return nil, err
	}
	return scanAIProvider(db.QueryRow(`SELECT `+aiProviderCols+`
		FROM ai_providers WHERE user_id = ? AND kind = ? AND slot = ?`, p.UserID, p.Kind, p.Slot))
}

// DeleteAIProvider removes one slot. Removing a slot in the middle leaves a
// gap rather than renumbering, because the chain is read in slot order and
// silently promoting a backup is not something to do behind someone's back.
func (db *DB) DeleteAIProvider(userID int64, kind string, slot int64) error {
	res, err := db.Exec(`DELETE FROM ai_providers WHERE user_id = ? AND kind = ? AND slot = ?`,
		userID, kind, slot)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AIProviderKey returns the stored key for one slot, for the balance lookup.
func (db *DB) AIProviderKey(userID int64, kind string, slot int64) (string, string, error) {
	var provider, key string
	err := db.QueryRow(`SELECT provider, api_key FROM ai_providers
		WHERE user_id = ? AND kind = ? AND slot = ?`, userID, kind, slot).Scan(&provider, &key)
	return provider, key, err
}
