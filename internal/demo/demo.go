// Package demo seeds and maintains the public demo account.
//
// The demo is a real account with real data, so every feature behaves exactly
// as it does for a signed-up user. Because anyone can sign into it and change
// things, the data is rebuilt from scratch on a schedule — visitors can click
// anything without spoiling it for the next person.
package demo

import (
	"fmt"
	"log"
	"math"
	"time"

	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/chem"
	"github.com/biswas-dev/pool/internal/store"
)

// Ensure creates the demo account if it is missing, then rebuilds its data.
func Ensure(db *store.DB, email, password string) error {
	user, err := db.UserByEmail(email)
	if err != nil {
		hash, err := auth.HashPassword(password)
		if err != nil {
			return fmt.Errorf("hash demo password: %w", err)
		}
		user, err = db.CreateUser(email, "Demo", hash, "demo")
		if err != nil {
			return fmt.Errorf("create demo user: %w", err)
		}
		log.Printf("created demo account %s", email)
	} else {
		// Keep the password in step with configuration, in case it changed.
		if hash, err := auth.HashPassword(password); err == nil {
			db.SetPassword(user.ID, hash)
		}
	}
	return Reset(db, user.ID)
}

// Reset deletes the demo account's pools and rebuilds them.
func Reset(db *store.DB, userID int64) error {
	pools, err := db.ListPools(userID)
	if err != nil {
		return err
	}
	for _, p := range pools {
		if err := db.DeletePool(userID, p.ID); err != nil {
			return fmt.Errorf("clear demo pool %d: %w", p.ID, err)
		}
	}
	return seed(db, userID)
}

// ResetLoop rebuilds the demo data periodically, so anything a visitor changes
// is short-lived.
func ResetLoop(db *store.DB, email string, every time.Duration) {
	for range time.Tick(every) {
		user, err := db.UserByEmail(email)
		if err != nil {
			continue
		}
		if err := Reset(db, user.ID); err != nil {
			log.Printf("demo reset: %v", err)
			continue
		}
		log.Print("demo data reset")
	}
}

func f(v float64) *float64 { return &v }

// reading is one dated water test in the demo history.
type reading struct {
	date   string
	values map[string]*float64
	note   string
}

// The demo tells a story across two seasons: a rocky opening, a well-behaved
// midsummer, then a salt cell that stops producing because the salt level
// drifts down — visible in the trends, explainable from the logbook.
var history = []reading{
	{date: "2025-05-12", values: map[string]*float64{"free_chlorine": f(0.8), "total_chlorine": f(1.1), "total_salt": f(2950), "ph": f(7.9), "total_alkalinity": f(140), "calcium_hardness": f(180), "cyanuric_acid": f(22), "temperature": f(17), "total_copper": f(0.05), "iron": f(0.02)},
		note: "Opened the pool. Water was green in the shallow end after the cover came off."},
	{date: "2025-06-14", values: map[string]*float64{"free_chlorine": f(2.1), "total_chlorine": f(2.3), "total_salt": f(3150), "ph": f(7.5), "total_alkalinity": f(110), "calcium_hardness": f(190), "cyanuric_acid": f(38), "temperature": f(24), "total_copper": f(0.04), "iron": f(0.02)}},
	{date: "2025-07-19", values: map[string]*float64{"free_chlorine": f(2.6), "total_chlorine": f(2.8), "total_salt": f(3210), "ph": f(7.5), "total_alkalinity": f(104), "calcium_hardness": f(196), "cyanuric_acid": f(42), "temperature": f(28), "total_copper": f(0.05), "iron": f(0.01)},
		note: "Best the water has looked all year."},
	{date: "2025-08-23", values: map[string]*float64{"free_chlorine": f(1.9), "total_chlorine": f(2.2), "total_salt": f(3180), "ph": f(7.6), "total_alkalinity": f(100), "calcium_hardness": f(200), "cyanuric_acid": f(40), "temperature": f(29), "total_copper": f(0.06), "iron": f(0.02)}},
	{date: "2025-09-27", values: map[string]*float64{"free_chlorine": f(1.4), "total_chlorine": f(1.6), "total_salt": f(3050), "ph": f(7.4), "total_alkalinity": f(96), "calcium_hardness": f(198), "cyanuric_acid": f(34), "temperature": f(21), "total_copper": f(0.08), "iron": f(0.03)}},
	{date: "2026-05-16", values: map[string]*float64{"free_chlorine": f(1.1), "total_chlorine": f(1.4), "total_salt": f(2780), "ph": f(7.8), "total_alkalinity": f(132), "calcium_hardness": f(176), "cyanuric_acid": f(18), "temperature": f(18), "total_copper": f(0.12), "iron": f(0.04)},
		note: "Second opening. Stabilizer washed out over the winter again."},
	{date: "2026-06-13", values: map[string]*float64{"free_chlorine": f(1.8), "total_chlorine": f(2.0), "total_salt": f(3100), "ph": f(7.5), "total_alkalinity": f(112), "calcium_hardness": f(170), "cyanuric_acid": f(35), "temperature": f(23), "total_copper": f(0.15), "iron": f(0.05)}},
	{date: "2026-07-04", values: map[string]*float64{"free_chlorine": f(1.2), "total_chlorine": f(1.6), "total_salt": f(2890), "ph": f(7.4), "total_alkalinity": f(108), "calcium_hardness": f(166), "cyanuric_acid": f(24), "temperature": f(26), "total_copper": f(0.19), "iron": f(0.07)}},
	{date: "2026-07-18", values: map[string]*float64{"free_chlorine": f(0.6), "total_chlorine": f(1.0), "total_salt": f(2510), "ph": f(7.35), "total_alkalinity": f(107), "calcium_hardness": f(162), "cyanuric_acid": f(12), "temperature": f(24), "total_copper": f(0.24), "iron": f(0.08)},
		note: "Cell is showing a low-salt fault. Called the shop out to look at it."},
	// The reference reading, taken from a real Pulse Hydrometrics report.
	{date: "2026-07-29", values: map[string]*float64{"free_chlorine": f(0.23), "total_chlorine": f(0.49), "combined_chlorine": f(0.26), "total_salt": f(2161), "ph": f(7.30), "total_alkalinity": f(106), "calcium_hardness": f(159), "cyanuric_acid": f(5), "phosphate": f(0), "temperature": f(21), "total_copper": f(0.30), "iron": f(0.10)},
		note: "Shop test. Written on the sheet: 1 jug of chlorine, 3 bags of salt, 10 pucks, 4 kg cal."},
}

// entry is one dated logbook line in the demo.
type entry struct {
	date     string
	category string
	item     string
	vendor   string
	qty      float64
	unit     string
	cents    int64
	note     string
}

var logbook = []entry{
	{"2025-05-10", "service", "Pool opening", "Jameson Pool & Spa", 0, "", 38500, "Cover off, pump and heater started for the season."},
	{"2025-05-10", "chemical", "Shock treatment", "Jameson Pool & Spa", 2, "kg", 4200, ""},
	{"2025-05-24", "chemical", "Pool salt", "Costco", 40, "kg", 3600, ""},
	{"2025-06-14", "chemical", "Stabilizer", "Jameson Pool & Spa", 2, "kg", 5400, ""},
	{"2025-06-28", "chemical", "Chlorine pucks", "Canadian Tire", 8, "kg", 9800, ""},
	{"2025-07-19", "chemical", "Muriatic acid", "Home Hardware", 4, "L", 2200, ""},
	{"2025-07-26", "equipment", "Cartridge filter", "Jameson Pool & Spa", 1, "each", 18900, "Old cartridge was collapsing at the seam."},
	{"2025-08-09", "utility", "Electricity (pump)", "", 0, "", 12400, ""},
	{"2025-08-23", "chemical", "Algaecide", "Canadian Tire", 1, "L", 3100, ""},
	{"2025-09-06", "utility", "Electricity (pump)", "", 0, "", 11800, ""},
	{"2025-10-04", "service", "Pool closing", "Jameson Pool & Spa", 0, "", 42500, ""},
	{"2025-10-04", "chemical", "Winterizing kit", "Jameson Pool & Spa", 1, "each", 7900, ""},
	{"2026-05-09", "service", "Pool opening", "Jameson Pool & Spa", 0, "", 41000, ""},
	{"2026-05-09", "chemical", "Shock treatment", "Jameson Pool & Spa", 2, "kg", 4400, ""},
	{"2026-05-16", "chemical", "Pool salt", "Costco", 60, "kg", 5400, ""},
	{"2026-05-30", "chemical", "Stabilizer", "Jameson Pool & Spa", 2, "kg", 5600, ""},
	{"2026-06-10", "chemical", "Chlorine", "Jameson Pool & Spa", 10, "L", 4250, ""},
	{"2026-06-11", "chemical", "Pool salt", "Costco", 20, "kg", 1900, ""},
	{"2026-06-20", "equipment", "Salt cell", "Jameson Pool & Spa", 1, "each", 89900, "Replaced the original cell after five seasons."},
	{"2026-06-20", "service", "Salt cell installation", "Jameson Pool & Spa", 1, "hour", 12000, ""},
	{"2026-07-04", "chemical", "Chlorine pucks", "Canadian Tire", 5, "kg", 6900, ""},
	{"2026-07-11", "utility", "Electricity (pump)", "", 0, "", 13200, ""},
	{"2026-07-18", "service", "Diagnostic call — cell not producing", "Jameson Pool & Spa", 1, "hour", 15000, "Cell tested fine. Salt was too low for it to run."},
	{"2026-07-20", "chemical", "Metal sequestrant", "Jameson Pool & Spa", 1, "L", 3400, ""},
	{"2026-07-29", "chemical", "Pool salt", "Costco", 60, "kg", 5400, ""},
	{"2026-07-29", "chemical", "Stabilizer", "Jameson Pool & Spa", 2, "kg", 5800, ""},
}

func seed(db *store.DB, userID int64) error {
	pool, err := db.CreatePool(&store.Pool{
		UserID: userID, Name: "Backyard pool", CustomerName: "Demo",
		SiteAddress: "Mississauga, ON", VolumeL: 58000,
		WaterType: "Swimming Pool", TreatmentProfile: "Salt (chlorine generator)",
		Grade: "Onground", Surface: "vinyl", Sanitizer: "salt", Location: "Outdoor", SaltPool: true,
	})
	if err != nil {
		return fmt.Errorf("create demo pool: %w", err)
	}

	if _, err := db.CreateSeason(&store.Season{PoolID: pool.ID, Name: "Season 2025", OpenedOn: "2025-05-10", ClosedOn: "2025-10-04"}); err != nil {
		return err
	}
	if _, err := db.CreateSeason(&store.Season{PoolID: pool.ID, Name: "Season 2026", OpenedOn: "2026-05-09"}); err != nil {
		return err
	}

	shop, err := db.CompanyByName(userID, "Jameson Pool & Spa", "store")
	if err != nil {
		return err
	}

	profile := pool.Profile()
	for _, r := range history {
		t := &store.Test{
			PoolID: pool.ID, CompanyID: &shop.ID, TestedAt: r.date + "T00:00:00Z",
			Operator: "Ryan", Source: "store",
			FreeChlorine: r.values["free_chlorine"], TotalChlorine: r.values["total_chlorine"],
			CombinedChlorine: r.values["combined_chlorine"], TotalSalt: r.values["total_salt"],
			PH: r.values["ph"], TotalAlkalinity: r.values["total_alkalinity"],
			CalciumHardness: r.values["calcium_hardness"], CyanuricAcid: r.values["cyanuric_acid"],
			Phosphate: r.values["phosphate"], Temperature: r.values["temperature"],
			TotalCopper: r.values["total_copper"], Iron: r.values["iron"],
		}
		derive(profile, t)

		created, err := db.CreateTest(t)
		if err != nil {
			return fmt.Errorf("create demo test %s: %w", r.date, err)
		}
		if err := saveTreatments(db, profile, created); err != nil {
			return err
		}
		if r.note != "" {
			if _, err := db.CreateNote(&store.Note{TestID: &created.ID, PoolID: pool.ID, UserID: &userID, Kind: "human", Body: r.note}); err != nil {
				return err
			}
		}
	}

	for _, e := range logbook {
		l := &store.LogEntry{
			PoolID: pool.ID, UserID: &userID, OccurredOn: e.date, Category: e.category,
			Item: e.item, Unit: e.unit, CostCents: e.cents, Vendor: e.vendor, Notes: e.note,
			Currency: "CAD",
		}
		if e.qty > 0 {
			q := e.qty
			l.Quantity = &q
		}
		if e.vendor == "Jameson Pool & Spa" {
			l.CompanyID = &shop.ID
		}
		if _, err := db.CreateLogEntry(l); err != nil {
			return fmt.Errorf("create demo log entry %s: %w", e.item, err)
		}
	}
	return nil
}

// derive fills in the computed fields for a demo test. It mirrors what the API
// does when a real test is submitted.
func derive(profile chem.Profile, t *store.Test) {
	if t.CombinedChlorine == nil && t.TotalChlorine != nil && t.FreeChlorine != nil {
		cc := math.Round((*t.TotalChlorine-*t.FreeChlorine)*100) / 100
		if cc >= 0 {
			t.CombinedChlorine = &cc
		}
	}
	if t.TotalAlkalinity != nil {
		adj := chem.AdjustedAlkalinity(*t.TotalAlkalinity, t.CyanuricAcid)
		t.TotalAlkalinityAdjusted = &adj
	}
	if t.PH != nil && t.TotalAlkalinity != nil && t.CalciumHardness != nil && t.Temperature != nil {
		lsi := chem.LSI(chem.LSIInput{
			PH: *t.PH, TotalAlkalinity: *t.TotalAlkalinity, CalciumHardness: *t.CalciumHardness,
			TemperatureC: *t.Temperature, CyanuricAcid: t.CyanuricAcid, TDS: t.TDS,
		})
		t.LSI = &lsi
	}
	score := int64(chem.ScoreOf(profile.Evaluate(t.Values()), t.LSI))
	t.Score = &score
}

func saveTreatments(db *store.DB, profile chem.Profile, t *store.Test) error {
	doses := profile.Doses(t.Values())
	ts := make([]store.Treatment, 0, len(doses))
	for _, d := range doses {
		amount := d.Amount
		ts = append(ts, store.Treatment{
			Parameter: d.Parameter, Product: d.Product, Amount: &amount, Unit: d.Unit,
			Reason: d.Reason, Note: d.Note, Priority: int64(d.Priority),
		})
	}
	return db.ReplaceComputedTreatments(t.ID, ts)
}
