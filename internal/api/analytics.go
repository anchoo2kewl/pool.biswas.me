package api

import (
	"net/http"
	"sort"

	"github.com/biswas-dev/pool/internal/chem"
	"github.com/biswas-dev/pool/internal/store"
)

// handleAnalyticsSummary returns the dashboard headline: the latest test, its
// verdict, outstanding actions, season spend, and testing cadence.
func (s *Server) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	pool, err := s.ownedPool(r, queryInt(r, "pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	out := map[string]any{"pool": pool}

	latest, err := s.DB.LatestTest(pool.ID)
	if err == nil {
		profile := pool.Profile()
		readings := profile.Evaluate(latest.Values())
		out["latest_test"] = latest
		out["readings"] = readings
		out["alerts"] = profile.Alerts(latest.Values(), latest.LSI)
		if treatments, err := s.DB.ListTreatments(latest.ID); err == nil {
			out["treatments"] = treatments
			pending := 0
			for _, t := range treatments {
				if !t.Applied {
					pending++
				}
			}
			out["pending_treatments"] = pending
		}
		if latest.LSI != nil {
			status, verdict := chem.LSIVerdict(*latest.LSI)
			out["lsi_status"], out["lsi_verdict"] = status, verdict
		}
	}

	// Seasons, with the current one highlighted.
	seasons, err := s.DB.ListSeasons(pool.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out["seasons"] = seasons
	var current *store.Season
	for i := range seasons {
		if seasons[i].ClosedOn == "" {
			current = &seasons[i]
			break
		}
	}
	if current == nil && len(seasons) > 0 {
		current = &seasons[0]
	}
	if current != nil {
		out["current_season"] = current
		cents, count, err := s.DB.CostTotal(store.CostFilter{PoolID: pool.ID, SeasonID: current.ID})
		if err == nil {
			out["season_spend_cents"] = cents
			out["season_entry_count"] = count
		}
		if byCat, err := s.DB.CostByCategory(store.CostFilter{PoolID: pool.ID, SeasonID: current.ID}); err == nil {
			out["season_by_category"] = byCat
		}
	}

	lifetimeCents, lifetimeCount, err := s.DB.CostTotal(store.CostFilter{PoolID: pool.ID})
	if err == nil {
		out["lifetime_spend_cents"] = lifetimeCents
		out["lifetime_entry_count"] = lifetimeCount
		// Cost per 10,000 L is the comparable figure between pools of
		// different sizes.
		if pool.VolumeL > 0 {
			out["cost_per_10k_l_cents"] = int64(float64(lifetimeCents) / (pool.VolumeL / 10000))
		}
	}

	if tests, err := s.DB.ListTests(u.ID, store.TestFilter{PoolID: pool.ID, Limit: 500}); err == nil {
		out["test_count"] = len(tests)
	}
	if recent, err := s.DB.ListLogEntries(u.ID, store.LogFilter{PoolID: pool.ID, Limit: 8}); err == nil {
		out["recent_log"] = recent
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAnalyticsCosts returns every cost breakdown the dashboard draws.
func (s *Server) handleAnalyticsCosts(w http.ResponseWriter, r *http.Request) {
	pool, err := s.ownedPool(r, queryInt(r, "pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := store.CostFilter{
		PoolID:   pool.ID,
		SeasonID: queryInt(r, "season_id"),
		From:     r.URL.Query().Get("from"),
		To:       r.URL.Query().Get("to"),
	}

	byCategory, err := s.DB.CostByCategory(f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	byMonth, err := s.DB.CostByMonth(f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	byItem, err := s.DB.CostByItem(f, 12)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	byVendor, err := s.DB.CostByVendor(f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	total, count, err := s.DB.CostTotal(f)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// The running total is computed here rather than in SQL, because the
	// embedded engine has no window functions.
	type point struct {
		Month           string `json:"month"`
		TotalCents      int64  `json:"total_cents"`
		CumulativeCents int64  `json:"cumulative_cents"`
	}
	cumulative := make([]point, 0, len(byMonth))
	var running int64
	for _, m := range byMonth {
		running += m.TotalCents
		cumulative = append(cumulative, point{Month: m.Month, TotalCents: m.TotalCents, CumulativeCents: running})
	}

	// Season-over-season comparison, oldest first so it reads left to right.
	seasons, _ := s.DB.ListSeasons(pool.ID)
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].OpenedOn < seasons[j].OpenedOn })

	writeJSON(w, http.StatusOK, map[string]any{
		"total_cents": total,
		"entry_count": count,
		"by_category": byCategory,
		"by_month":    byMonth,
		"by_item":     byItem,
		"by_vendor":   byVendor,
		"cumulative":  cumulative,
		"seasons":     seasons,
		"currency":    "CAD",
	})
}

// handleAnalyticsTrends returns per-parameter time series with their ideal
// bands, plus the logbook events to overlay on them.
func (s *Server) handleAnalyticsTrends(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	pool, err := s.ownedPool(r, queryInt(r, "pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := int(queryInt(r, "limit"))
	if limit <= 0 {
		limit = 200
	}
	tests, err := s.DB.ListTests(u.ID, store.TestFilter{
		PoolID: pool.ID, From: r.URL.Query().Get("from"), To: r.URL.Query().Get("to"), Limit: limit,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Oldest first, so a chart reads left to right.
	sort.Slice(tests, func(i, j int) bool { return tests[i].TestedAt < tests[j].TestedAt })

	profile := pool.Profile()
	ranges := profile.Ranges()

	// One series per parameter, carrying its ideal band for the chart to draw.
	type sample struct {
		At    string   `json:"at"`
		Value *float64 `json:"value"`
	}
	type series struct {
		Key    string     `json:"key"`
		Label  string     `json:"label"`
		Unit   string     `json:"unit"`
		Group  string     `json:"group"`
		Ideal  [2]float64 `json:"ideal"`
		Target float64    `json:"target"`
		Points []sample   `json:"points"`
	}

	out := make([]series, 0, len(ranges)+2)
	for _, rg := range ranges {
		se := series{Key: rg.Key, Label: rg.Label, Unit: rg.Unit, Group: rg.Group, Ideal: rg.Ideal, Target: rg.Target}
		for i := range tests {
			v := tests[i].Values()[rg.Key]
			se.Points = append(se.Points, sample{At: tests[i].TestedAt, Value: v})
		}
		out = append(out, se)
	}

	// The saturation index and the overall score are derived, not measured, so
	// they are appended rather than coming from the range list.
	lsi := series{Key: "lsi", Label: "Saturation Index", Unit: "", Group: "derived", Ideal: [2]float64{-0.3, 0.3}, Target: 0}
	score := series{Key: "score", Label: "Water Quality Score", Unit: "/100", Group: "derived", Ideal: [2]float64{80, 100}, Target: 100}
	for i := range tests {
		lsi.Points = append(lsi.Points, sample{At: tests[i].TestedAt, Value: tests[i].LSI})
		var sv *float64
		if tests[i].Score != nil {
			f := float64(*tests[i].Score)
			sv = &f
		}
		score.Points = append(score.Points, sample{At: tests[i].TestedAt, Value: sv})
	}
	out = append(out, lsi, score)

	// Logbook events overlay the chemistry charts, so a spike can be read
	// against what was added.
	events, _ := s.DB.ListLogEntries(u.ID, store.LogFilter{PoolID: pool.ID, From: r.URL.Query().Get("from"), To: r.URL.Query().Get("to")})

	cadence, _ := s.DB.TestCadence(pool.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"pool":    pool,
		"series":  out,
		"tests":   tests,
		"events":  events,
		"cadence": cadence,
	})
}
