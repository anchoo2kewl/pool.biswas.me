package api

import (
	"math"
	"net/http"
	"strings"

	"github.com/biswas-dev/pool/internal/store"
)

// ── Seasons ──────────────────────────────────────────────────────────────

func (s *Server) handleListSeasons(w http.ResponseWriter, r *http.Request) {
	p, err := s.ownedPool(r, queryInt(r, "pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	seasons, err := s.DB.ListSeasons(p.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seasons)
}

type seasonRequest struct {
	PoolID   int64  `json:"pool_id"`
	Name     string `json:"name"`
	OpenedOn string `json:"opened_on"`
	ClosedOn string `json:"closed_on"`
	Notes    string `json:"notes"`
}

func (s *Server) handleCreateSeason(w http.ResponseWriter, r *http.Request) {
	var req seasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.ownedPool(r, req.PoolID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opened, err := normaliseDate(req.OpenedOn)
	if err != nil {
		writeError(w, http.StatusBadRequest, "opened_on: "+err.Error())
		return
	}
	closed := ""
	if strings.TrimSpace(req.ClosedOn) != "" {
		if closed, err = normaliseDate(req.ClosedOn); err != nil {
			writeError(w, http.StatusBadRequest, "closed_on: "+err.Error())
			return
		}
		if closed < opened {
			writeError(w, http.StatusBadRequest, "closed_on cannot be before opened_on")
			return
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Season " + opened[:4]
	}
	created, err := s.DB.CreateSeason(&store.Season{PoolID: p.ID, Name: name, OpenedOn: opened, ClosedOn: closed, Notes: req.Notes})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateSeason(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid season id")
		return
	}
	var req seasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.ownedPool(r, req.PoolID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	season, err := s.DB.Season(p.ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if req.Name != "" {
		season.Name = req.Name
	}
	if req.OpenedOn != "" {
		if season.OpenedOn, err = normaliseDate(req.OpenedOn); err != nil {
			writeError(w, http.StatusBadRequest, "opened_on: "+err.Error())
			return
		}
	}
	if req.ClosedOn != "" {
		if season.ClosedOn, err = normaliseDate(req.ClosedOn); err != nil {
			writeError(w, http.StatusBadRequest, "closed_on: "+err.Error())
			return
		}
	}
	season.Notes = req.Notes
	if err := s.DB.UpdateSeason(season); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, season)
}

func (s *Server) handleDeleteSeason(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid season id")
		return
	}
	p, err := s.ownedPool(r, queryInt(r, "pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.DeleteSeason(p.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Log entries ──────────────────────────────────────────────────────────

func (s *Server) handleListLog(w http.ResponseWriter, r *http.Request) {
	f := store.LogFilter{
		PoolID:   queryInt(r, "pool_id"),
		SeasonID: queryInt(r, "season_id"),
		Category: r.URL.Query().Get("category"),
		From:     r.URL.Query().Get("from"),
		To:       r.URL.Query().Get("to"),
		Limit:    int(queryInt(r, "limit")),
	}
	entries, err := s.DB.ListLogEntries(userFrom(r).ID, f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// logRequest accepts either cost (major units, e.g. 42.50) or cost_cents.
// Users think in dollars; scripts often have exact cents.
type logRequest struct {
	PoolID     int64    `json:"pool_id"`
	CompanyID  *int64   `json:"company_id"`
	VendorName string   `json:"vendor_name"`
	TestID     *int64   `json:"test_id"`
	OccurredOn string   `json:"occurred_on"`
	Category   string   `json:"category"`
	Item       string   `json:"item"`
	Quantity   *float64 `json:"quantity"`
	Unit       string   `json:"unit"`
	Cost       *float64 `json:"cost"`
	CostCents  *int64   `json:"cost_cents"`
	Currency   string   `json:"currency"`
	Vendor     string   `json:"vendor"`
	Notes      string   `json:"notes"`
	ReceiptIDs []int64  `json:"receipt_ids"`
}

func (req logRequest) cents() int64 {
	if req.CostCents != nil {
		return *req.CostCents
	}
	if req.Cost != nil {
		return int64(math.Round(*req.Cost * 100))
	}
	return 0
}

func (s *Server) handleCreateLog(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req logRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.ownedPool(r, req.PoolID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Item) == "" {
		writeError(w, http.StatusBadRequest, "item is required")
		return
	}
	category := strings.ToLower(strings.TrimSpace(req.Category))
	if category == "" {
		category = "chemical"
	}
	if !store.ValidCategory(category) {
		writeError(w, http.StatusBadRequest, "category must be one of: "+strings.Join(store.Categories, ", "))
		return
	}
	date := req.OccurredOn
	if strings.TrimSpace(date) == "" {
		date = store.Today()
	} else if date, err = normaliseDate(date); err != nil {
		writeError(w, http.StatusBadRequest, "occurred_on: "+err.Error())
		return
	}

	entry := &store.LogEntry{
		PoolID: p.ID, CompanyID: req.CompanyID, TestID: req.TestID, UserID: &u.ID,
		OccurredOn: date, Category: category, Item: strings.TrimSpace(req.Item),
		Quantity: req.Quantity, Unit: req.Unit, CostCents: req.cents(),
		Currency: orDefault(req.Currency, "CAD"), Vendor: req.Vendor, Notes: req.Notes,
	}
	// A vendor can be named instead of referenced, matching the tests API.
	if entry.CompanyID == nil && strings.TrimSpace(req.VendorName) != "" {
		if c, err := s.DB.CompanyByName(u.ID, strings.TrimSpace(req.VendorName), "supplier"); err == nil {
			entry.CompanyID = &c.ID
			if entry.Vendor == "" {
				entry.Vendor = c.Name
			}
		}
	}

	created, err := s.DB.CreateLogEntry(entry)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, rid := range req.ReceiptIDs {
		if _, err := s.DB.Receipt(u.ID, rid); err == nil {
			s.DB.LinkReceipt(rid, created.ID)
		}
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateLog(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid entry id")
		return
	}
	poolID, err := s.DB.LogEntryOwner(u.ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	entry, err := s.DB.LogEntryByID(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req logRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry.PoolID = poolID
	if req.OccurredOn != "" {
		if entry.OccurredOn, err = normaliseDate(req.OccurredOn); err != nil {
			writeError(w, http.StatusBadRequest, "occurred_on: "+err.Error())
			return
		}
	}
	if req.Category != "" {
		c := strings.ToLower(req.Category)
		if !store.ValidCategory(c) {
			writeError(w, http.StatusBadRequest, "category must be one of: "+strings.Join(store.Categories, ", "))
			return
		}
		entry.Category = c
	}
	if req.Item != "" {
		entry.Item = req.Item
	}
	if req.Quantity != nil {
		entry.Quantity = req.Quantity
	}
	if req.Unit != "" {
		entry.Unit = req.Unit
	}
	if req.Cost != nil || req.CostCents != nil {
		entry.CostCents = req.cents()
	}
	if req.Currency != "" {
		entry.Currency = req.Currency
	}
	if req.Vendor != "" {
		entry.Vendor = req.Vendor
	}
	if req.CompanyID != nil {
		entry.CompanyID = req.CompanyID
	}
	if req.TestID != nil {
		entry.TestID = req.TestID
	}
	entry.Notes = req.Notes

	if err := s.DB.UpdateLogEntry(entry); err != nil {
		writeStoreError(w, err)
		return
	}
	updated, err := s.DB.LogEntryByID(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteLog(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid entry id")
		return
	}
	poolID, err := s.DB.LogEntryOwner(userFrom(r).ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.DB.DeleteLogEntry(poolID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
