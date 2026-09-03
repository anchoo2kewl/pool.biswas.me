package store

// User is an account. Passwords are argon2id; ai_api_key holds the user's own
// LLM credential so insights are billed to them, not to the server.
type User struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	AIAPIKey    string `json:"-"`
	AIBaseURL   string `json:"ai_base_url"`
	AIModel     string `json:"ai_model"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at,omitempty"`
	// HasAIKey reports whether an LLM key is configured, without exposing it.
	HasAIKey bool `json:"has_ai_key"`
	// MFAEnabled reports whether a confirmed second factor stands between a
	// password and a session.
	MFAEnabled bool `json:"mfa_enabled"`
}

// Company is a pool store, service contractor, or the owner themselves.
type Company struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
	Address   string `json:"address"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
}

// Pool is the body of water being tracked.
type Pool struct {
	ID               int64   `json:"id"`
	UserID           int64   `json:"-"`
	Name             string  `json:"name"`
	CustomerName     string  `json:"customer_name"`
	SiteAddress      string  `json:"site_address"`
	VolumeL          float64 `json:"volume_l"`
	WaterType        string  `json:"water_type"`
	TreatmentProfile string  `json:"treatment_profile"`
	Grade            string  `json:"grade"`
	Surface          string  `json:"surface"`
	Sanitizer        string  `json:"sanitizer"`
	Location         string  `json:"location"`
	SaltPool         bool    `json:"salt_pool"`
	CreatedAt        string  `json:"created_at"`
}

// Test is one water analysis. Every reading is optional.
type Test struct {
	ID        int64  `json:"id"`
	PoolID    int64  `json:"pool_id"`
	CompanyID *int64 `json:"company_id"`
	TestedAt  string `json:"tested_at"`
	Operator  string `json:"operator"`
	TestCount *int64 `json:"test_count"`
	Source    string `json:"source"`

	FreeChlorine     *float64 `json:"free_chlorine"`
	TotalChlorine    *float64 `json:"total_chlorine"`
	CombinedChlorine *float64 `json:"combined_chlorine"`
	TotalSalt        *float64 `json:"total_salt"`
	Bromine          *float64 `json:"bromine"`

	PH                      *float64 `json:"ph"`
	TotalAlkalinity         *float64 `json:"total_alkalinity"`
	TotalAlkalinityAdjusted *float64 `json:"total_alkalinity_adjusted"`
	CalciumHardness         *float64 `json:"calcium_hardness"`

	CyanuricAcid *float64 `json:"cyanuric_acid"`
	Phosphate    *float64 `json:"phosphate"`
	Borate       *float64 `json:"borate"`
	TDS          *float64 `json:"tds"`
	Temperature  *float64 `json:"temperature"`

	TotalCopper    *float64 `json:"total_copper"`
	FreeCopper     *float64 `json:"free_copper"`
	CombinedCopper *float64 `json:"combined_copper"`
	Iron           *float64 `json:"iron"`

	WQI       *float64 `json:"wqi"`
	LSI       *float64 `json:"lsi"`
	Score     *int64   `json:"score"`
	Weather   string   `json:"weather"`
	CreatedAt string   `json:"created_at"`

	// Joined for convenience on read.
	CompanyName string `json:"company_name,omitempty"`
}

// Values maps the test's readings onto the keys the chem package uses.
func (t *Test) Values() map[string]*float64 {
	return map[string]*float64{
		"free_chlorine":     t.FreeChlorine,
		"total_chlorine":    t.TotalChlorine,
		"combined_chlorine": t.CombinedChlorine,
		"total_salt":        t.TotalSalt,
		"bromine":           t.Bromine,
		"ph":                t.PH,
		"total_alkalinity":  t.TotalAlkalinity,
		"calcium_hardness":  t.CalciumHardness,
		"cyanuric_acid":     t.CyanuricAcid,
		"phosphate":         t.Phosphate,
		"borate":            t.Borate,
		"tds":               t.TDS,
		"temperature":       t.Temperature,
		"total_copper":      t.TotalCopper,
		"free_copper":       t.FreeCopper,
		"combined_copper":   t.CombinedCopper,
		"iron":              t.Iron,
	}
}

// Note is a human comment or an AI-generated analysis.
type Note struct {
	ID        int64  `json:"id"`
	TestID    *int64 `json:"test_id"`
	PoolID    int64  `json:"pool_id"`
	UserID    *int64 `json:"user_id"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	Model     string `json:"model,omitempty"`
	Meta      string `json:"meta,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Treatment is a recommended chemical addition for a test.
type Treatment struct {
	ID        int64    `json:"id"`
	TestID    int64    `json:"test_id"`
	Parameter string   `json:"parameter"`
	Product   string   `json:"product"`
	Amount    *float64 `json:"amount"`
	Unit      string   `json:"unit"`
	Reason    string   `json:"reason"`
	Note      string   `json:"note"`
	Priority  int64    `json:"priority"`
	Source    string   `json:"source"`
	Applied   bool     `json:"applied"`
	AppliedAt string   `json:"applied_at,omitempty"`
	CreatedAt string   `json:"created_at"`
}

// Season is the operating window a pool's costs roll up to.
type Season struct {
	ID        int64  `json:"id"`
	PoolID    int64  `json:"pool_id"`
	Name      string `json:"name"`
	OpenedOn  string `json:"opened_on"`
	ClosedOn  string `json:"closed_on,omitempty"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`

	// Rolled up on read.
	TotalCents int64 `json:"total_cents"`
	EntryCount int64 `json:"entry_count"`
}

// LogEntry is anything that went into, onto, or was done to the pool.
type LogEntry struct {
	ID         int64    `json:"id"`
	PoolID     int64    `json:"pool_id"`
	SeasonID   *int64   `json:"season_id"`
	CompanyID  *int64   `json:"company_id"`
	TestID     *int64   `json:"test_id"`
	UserID     *int64   `json:"user_id"`
	OccurredOn string   `json:"occurred_on"`
	Category   string   `json:"category"`
	Item       string   `json:"item"`
	Quantity   *float64 `json:"quantity"`
	Unit       string   `json:"unit"`
	CostCents  int64    `json:"cost_cents"`
	Currency   string   `json:"currency"`
	Vendor     string   `json:"vendor"`
	Notes      string   `json:"notes"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`

	// Joined on read.
	CompanyName string  `json:"company_name,omitempty"`
	SeasonName  string  `json:"season_name,omitempty"`
	ReceiptIDs  []int64 `json:"receipt_ids,omitempty"`
}

// Cost returns the entry's cost in major currency units.
func (l LogEntry) Cost() float64 { return float64(l.CostCents) / 100 }

// Receipt is an uploaded document backing one or more log entries.
type Receipt struct {
	ID          int64  `json:"id"`
	PoolID      int64  `json:"pool_id"`
	UserID      *int64 `json:"user_id"`
	Filename    string `json:"filename"`
	StoredName  string `json:"-"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	TotalCents  int64  `json:"total_cents"`
	Currency    string `json:"currency"`
	Vendor      string `json:"vendor"`
	PurchasedOn string `json:"purchased_on,omitempty"`
	Notes       string `json:"notes"`
	CreatedAt   string `json:"created_at"`

	// Kind is "receipt" for a purchase document or "test_sheet" for a scanned
	// water analysis. Both are stored the same way.
	Kind   string `json:"kind"`
	TestID *int64 `json:"test_id,omitempty"`
	Width  int64  `json:"width,omitempty"`
	Height int64  `json:"height,omitempty"`
	// OriginalBytes is the upload size before recompression, so the UI can
	// show how much storage was saved.
	OriginalBytes int64 `json:"original_bytes,omitempty"`

	LinkedEntryIDs []int64 `json:"linked_entry_ids,omitempty"`
}

// APIKey is a named credential for programmatic access. The plaintext is
// returned exactly once, at creation.
type APIKey struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"-"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Scopes     string `json:"scopes"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	// Key is populated only in the response that creates it.
	Key string `json:"key,omitempty"`
}

// Invite is a single-use registration code.
type Invite struct {
	Code      string `json:"code"`
	CreatedBy *int64 `json:"created_by"`
	Email     string `json:"email"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at,omitempty"`
	UsedAt    string `json:"used_at,omitempty"`
	UsedBy    *int64 `json:"used_by,omitempty"`
}

// Categories are the log-entry buckets, in the order they appear in the UI.
var Categories = []string{"chemical", "equipment", "service", "maintenance", "utility", "opening", "closing", "other"}

// ValidCategory reports whether c is a known log-entry category.
func ValidCategory(c string) bool {
	for _, k := range Categories {
		if k == c {
			return true
		}
	}
	return false
}
