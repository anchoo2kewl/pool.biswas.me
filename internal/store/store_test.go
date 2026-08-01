package store

import (
	"path/filepath"
	"testing"
)

// open gives each test its own database file.
func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func fixture(t *testing.T) (*DB, *User, *Pool) {
	t.Helper()
	db := open(t)
	u, err := db.CreateUser("owner@example.com", "Owner", "hash", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	p, err := db.CreatePool(&Pool{UserID: u.ID, Name: "Backyard", VolumeL: 58000, Surface: "vinyl", Sanitizer: "salt", SaltPool: true})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	return db, u, p
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db.Close()
	// Re-opening must not try to re-apply anything.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != len(migrations) {
		t.Errorf("applied %d migrations, want %d", n, len(migrations))
	}
}

func TestUserLookupIsCaseInsensitive(t *testing.T) {
	db := open(t)
	if _, err := db.CreateUser("Mixed@Example.COM", "M", "hash", "member"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.UserByEmail("mixed@example.com"); err != nil {
		t.Errorf("lookup by lowercase failed: %v", err)
	}
	if _, err := db.UserByEmail("MIXED@EXAMPLE.COM"); err != nil {
		t.Errorf("lookup by uppercase failed: %v", err)
	}
}

func TestPoolsAreScopedToTheirOwner(t *testing.T) {
	db, _, p := fixture(t)
	other, err := db.CreateUser("other@example.com", "Other", "hash", "member")
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if _, err := db.Pool(other.ID, p.ID); err == nil {
		t.Fatal("another user was able to read this pool")
	}
	if err := db.DeletePool(other.ID, p.ID); err == nil {
		t.Fatal("another user was able to delete this pool")
	}
}

// A backdated entry has to land in whichever season contains its date, and a
// season created afterwards has to adopt the entries that fall inside it.
func TestBackdatedEntriesFileIntoTheRightSeason(t *testing.T) {
	db, u, p := fixture(t)

	// Logged before any season exists.
	e, err := db.CreateLogEntry(&LogEntry{PoolID: p.ID, UserID: &u.ID, OccurredOn: "2026-06-10",
		Category: "chemical", Item: "Chlorine", CostCents: 4250})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if e.SeasonID != nil {
		t.Errorf("entry got season %v before any season existed", *e.SeasonID)
	}

	s2025, err := db.CreateSeason(&Season{PoolID: p.ID, Name: "2025", OpenedOn: "2025-05-10", ClosedOn: "2025-10-04"})
	if err != nil {
		t.Fatalf("create 2025: %v", err)
	}
	s2026, err := db.CreateSeason(&Season{PoolID: p.ID, Name: "2026", OpenedOn: "2026-05-09"})
	if err != nil {
		t.Fatalf("create 2026: %v", err)
	}

	got, err := db.LogEntryByID(e.ID)
	if err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if got.SeasonID == nil {
		t.Fatal("entry was not adopted by a season")
	}
	if *got.SeasonID != s2026.ID {
		t.Errorf("entry filed into season %d, want 2026 (%d)", *got.SeasonID, s2026.ID)
	}

	// An entry inside the closed 2025 window goes to 2025, not the open season.
	old, err := db.CreateLogEntry(&LogEntry{PoolID: p.ID, UserID: &u.ID, OccurredOn: "2025-07-04",
		Category: "chemical", Item: "Salt", CostCents: 3600})
	if err != nil {
		t.Fatalf("create backdated entry: %v", err)
	}
	if old.SeasonID == nil || *old.SeasonID != s2025.ID {
		t.Errorf("2025-07-04 entry filed into %v, want 2025 (%d)", old.SeasonID, s2025.ID)
	}

	// A date in neither window stays unfiled rather than guessing.
	gap, err := db.CreateLogEntry(&LogEntry{PoolID: p.ID, UserID: &u.ID, OccurredOn: "2026-01-15",
		Category: "utility", Item: "Winter cover", CostCents: 1000})
	if err != nil {
		t.Fatalf("create gap entry: %v", err)
	}
	if gap.SeasonID != nil {
		t.Errorf("off-season entry was filed into season %d", *gap.SeasonID)
	}
}

func TestCostRollups(t *testing.T) {
	db, u, p := fixture(t)
	season, err := db.CreateSeason(&Season{PoolID: p.ID, Name: "2026", OpenedOn: "2026-05-01"})
	if err != nil {
		t.Fatalf("create season: %v", err)
	}
	entries := []LogEntry{
		{OccurredOn: "2026-05-10", Category: "service", Item: "Pool opening", CostCents: 38500, Vendor: "Jameson"},
		{OccurredOn: "2026-06-10", Category: "chemical", Item: "Chlorine", CostCents: 4250, Vendor: "Jameson"},
		{OccurredOn: "2026-06-11", Category: "chemical", Item: "Chlorine", CostCents: 4250, Vendor: "Costco"},
		{OccurredOn: "2026-06-20", Category: "equipment", Item: "Salt cell", CostCents: 89900, Vendor: "Jameson"},
	}
	for i := range entries {
		entries[i].PoolID = p.ID
		entries[i].UserID = &u.ID
		if _, err := db.CreateLogEntry(&entries[i]); err != nil {
			t.Fatalf("create entry %d: %v", i, err)
		}
	}

	total, count, err := db.CostTotal(CostFilter{PoolID: p.ID})
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if want := int64(38500 + 4250 + 4250 + 89900); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}

	byCat, err := db.CostByCategory(CostFilter{PoolID: p.ID})
	if err != nil {
		t.Fatalf("by category: %v", err)
	}
	// Ordered by spend, so equipment (the salt cell) leads.
	if len(byCat) != 3 || byCat[0].Category != "equipment" || byCat[0].TotalCents != 89900 {
		t.Errorf("by category = %+v, want equipment first at 89900", byCat)
	}

	byItem, err := db.CostByItem(CostFilter{PoolID: p.ID}, 10)
	if err != nil {
		t.Fatalf("by item: %v", err)
	}
	var chlorine *ItemTotal
	for i := range byItem {
		if byItem[i].Item == "Chlorine" {
			chlorine = &byItem[i]
		}
	}
	if chlorine == nil {
		t.Fatal("Chlorine missing from item rollup")
	}
	if chlorine.TotalCents != 8500 || chlorine.Count != 2 {
		t.Errorf("Chlorine = %d cents over %d purchases, want 8500 over 2", chlorine.TotalCents, chlorine.Count)
	}

	byMonth, err := db.CostByMonth(CostFilter{PoolID: p.ID})
	if err != nil {
		t.Fatalf("by month: %v", err)
	}
	if len(byMonth) != 2 || byMonth[0].Month != "2026-05" {
		t.Errorf("by month = %+v, want 2026-05 first", byMonth)
	}

	// Filtering by season must not change the total here, since every entry
	// falls inside it.
	seasonTotal, _, err := db.CostTotal(CostFilter{PoolID: p.ID, SeasonID: season.ID})
	if err != nil {
		t.Fatalf("season total: %v", err)
	}
	if seasonTotal != total {
		t.Errorf("season total = %d, want %d", seasonTotal, total)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	db, u, _ := fixture(t)
	k, err := db.CreateAPIKey(u.ID, "script", "pool_sk_abc", "hash-value", "read,write", "")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	got, _, err := db.UserByAPIKeyHash("hash-value")
	if err != nil {
		t.Fatalf("resolve key: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("key resolved to user %d, want %d", got.ID, u.ID)
	}

	if err := db.RevokeAPIKey(u.ID, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := db.UserByAPIKeyHash("hash-value"); err == nil {
		t.Error("a revoked key still authenticates")
	}
}

func TestOAuthIdentityLinking(t *testing.T) {
	db, u, _ := fixture(t)
	if err := db.LinkIdentity("google", "sub-123", u.ID, "owner@example.com", ""); err != nil {
		t.Fatalf("link: %v", err)
	}
	got, err := db.UserByIdentity("google", "sub-123")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("identity resolved to user %d, want %d", got.ID, u.ID)
	}
	// Linking the same identity again updates rather than failing.
	if err := db.LinkIdentity("google", "sub-123", u.ID, "new@example.com", "avatar"); err != nil {
		t.Errorf("re-linking failed: %v", err)
	}
	providers, err := db.ListIdentities(u.ID)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(providers) != 1 || providers[0] != "google" {
		t.Errorf("providers = %v, want [google]", providers)
	}
}

func TestLatestTestOrdersByTestDateNotInsertion(t *testing.T) {
	db, _, p := fixture(t)
	ph := 7.4
	// Inserted newest-first, so insertion order and test order disagree.
	if _, err := db.CreateTest(&Test{PoolID: p.ID, TestedAt: "2026-07-29T00:00:00Z", PH: &ph}); err != nil {
		t.Fatalf("create test: %v", err)
	}
	if _, err := db.CreateTest(&Test{PoolID: p.ID, TestedAt: "2026-06-13T00:00:00Z", PH: &ph}); err != nil {
		t.Fatalf("create test: %v", err)
	}
	latest, err := db.LatestTest(p.ID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.TestedAt != "2026-07-29T00:00:00Z" {
		t.Errorf("latest test = %s, want the July one", latest.TestedAt)
	}
}

func TestDeletingAPoolCascades(t *testing.T) {
	db, u, p := fixture(t)
	ph := 7.5
	if _, err := db.CreateTest(&Test{PoolID: p.ID, TestedAt: "2026-07-01T00:00:00Z", PH: &ph}); err != nil {
		t.Fatalf("create test: %v", err)
	}
	if _, err := db.CreateLogEntry(&LogEntry{PoolID: p.ID, UserID: &u.ID, OccurredOn: "2026-07-01",
		Category: "chemical", Item: "Chlorine", CostCents: 1000}); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if err := db.DeletePool(u.ID, p.ID); err != nil {
		t.Fatalf("delete pool: %v", err)
	}

	var tests, entries int
	db.QueryRow(`SELECT COUNT(*) FROM tests WHERE pool_id = ?`, p.ID).Scan(&tests)
	db.QueryRow(`SELECT COUNT(*) FROM log_entries WHERE pool_id = ?`, p.ID).Scan(&entries)
	if tests != 0 || entries != 0 {
		t.Errorf("after deleting the pool: %d tests and %d entries remain", tests, entries)
	}
}

func TestSplitSQLIgnoresSemicolonsInStrings(t *testing.T) {
	got := splitSQL(`CREATE TABLE a(x TEXT DEFAULT 'one;two'); CREATE TABLE b(y INT);`)
	if len(got) != 2 {
		t.Fatalf("split into %d statements, want 2: %q", len(got), got)
	}
	if !contains(got[0], "one;two") {
		t.Errorf("the string literal was split: %q", got[0])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
