package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/biswas-dev/pool/internal/chem"
	"github.com/biswas-dev/pool/internal/store"
)

// ── Pools ────────────────────────────────────────────────────────────────

func (s *Server) handleListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.DB.ListPools(userFrom(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pools)
}

type poolRequest struct {
	Name             string   `json:"name"`
	CustomerName     string   `json:"customer_name"`
	SiteAddress      string   `json:"site_address"`
	VolumeL          *float64 `json:"volume_l"`
	WaterType        string   `json:"water_type"`
	TreatmentProfile string   `json:"treatment_profile"`
	Grade            string   `json:"grade"`
	Surface          string   `json:"surface"`
	Sanitizer        string   `json:"sanitizer"`
	Location         string   `json:"location"`
	SaltPool         *bool    `json:"salt_pool"`
}

func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req poolRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.VolumeL == nil || *req.VolumeL <= 0 {
		writeError(w, http.StatusBadRequest, "volume_l is required and must be greater than zero")
		return
	}

	p := &store.Pool{
		UserID: u.ID, Name: strings.TrimSpace(req.Name), CustomerName: req.CustomerName,
		SiteAddress: req.SiteAddress, VolumeL: *req.VolumeL,
		WaterType:        orDefault(req.WaterType, "Swimming Pool"),
		TreatmentProfile: req.TreatmentProfile,
		Grade:            req.Grade,
		Surface:          orDefault(strings.ToLower(req.Surface), "vinyl"),
		Sanitizer:        orDefault(strings.ToLower(req.Sanitizer), "chlorine"),
		Location:         orDefault(req.Location, "Outdoor"),
	}
	if req.SaltPool != nil {
		p.SaltPool = *req.SaltPool
	} else {
		p.SaltPool = p.Sanitizer == "salt"
	}

	created, err := s.DB.CreatePool(p)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a pool with that name already exists")
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pool id")
		return
	}
	p, err := s.DB.Pool(userFrom(r).ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpdatePool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pool id")
		return
	}
	p, err := s.DB.Pool(userFrom(r).ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req poolRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name != "" {
		p.Name = strings.TrimSpace(req.Name)
	}
	if req.VolumeL != nil && *req.VolumeL > 0 {
		p.VolumeL = *req.VolumeL
	}
	if req.CustomerName != "" {
		p.CustomerName = req.CustomerName
	}
	if req.SiteAddress != "" {
		p.SiteAddress = req.SiteAddress
	}
	if req.WaterType != "" {
		p.WaterType = req.WaterType
	}
	if req.TreatmentProfile != "" {
		p.TreatmentProfile = req.TreatmentProfile
	}
	if req.Grade != "" {
		p.Grade = req.Grade
	}
	if req.Surface != "" {
		p.Surface = strings.ToLower(req.Surface)
	}
	if req.Sanitizer != "" {
		p.Sanitizer = strings.ToLower(req.Sanitizer)
	}
	if req.Location != "" {
		p.Location = req.Location
	}
	if req.SaltPool != nil {
		p.SaltPool = *req.SaltPool
	}
	if err := s.DB.UpdatePool(p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pool id")
		return
	}
	// Collect the attachment filenames before the rows go, or the files are
	// orphaned on disk with nothing left pointing at them.
	stored, err := s.DB.StoredNamesForPool(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.DB.DeletePool(userFrom(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.removeOrphanedFiles(stored)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Companies ────────────────────────────────────────────────────────────

func (s *Server) handleListCompanies(w http.ResponseWriter, r *http.Request) {
	cs, err := s.DB.ListCompanies(userFrom(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) handleCreateCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Phone   string `json:"phone"`
		Email   string `json:"email"`
		Address string `json:"address"`
		Notes   string `json:"notes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	c, err := s.DB.CreateCompany(&store.Company{
		UserID: userFrom(r).ID, Name: strings.TrimSpace(req.Name), Kind: orDefault(req.Kind, "store"),
		Phone: req.Phone, Email: req.Email, Address: req.Address, Notes: req.Notes,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a company with that name already exists")
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateCompany(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid company id")
		return
	}
	c, err := s.DB.Company(userFrom(r).ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Phone   string `json:"phone"`
		Email   string `json:"email"`
		Address string `json:"address"`
		Notes   string `json:"notes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name != "" {
		c.Name = req.Name
	}
	if req.Kind != "" {
		c.Kind = req.Kind
	}
	c.Phone, c.Email, c.Address, c.Notes = req.Phone, req.Email, req.Address, req.Notes
	if err := s.DB.UpdateCompany(c); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteCompany(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid company id")
		return
	}
	if err := s.DB.DeleteCompany(userFrom(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Tests ────────────────────────────────────────────────────────────────

// testRequest accepts a water analysis. Every reading is optional, so a strip
// test and a full photometer panel use the same endpoint.
type testRequest struct {
	PoolID      int64    `json:"pool_id"`
	CompanyID   *int64   `json:"company_id"`
	CompanyName string   `json:"company_name"`
	TestedAt    string   `json:"tested_at"`
	Operator    string   `json:"operator"`
	TestCount   *int64   `json:"test_count"`
	Source      string   `json:"source"`
	Weather     string   `json:"weather"`
	WQI         *float64 `json:"wqi"`

	FreeChlorine     *float64 `json:"free_chlorine"`
	TotalChlorine    *float64 `json:"total_chlorine"`
	CombinedChlorine *float64 `json:"combined_chlorine"`
	TotalSalt        *float64 `json:"total_salt"`
	Bromine          *float64 `json:"bromine"`

	PH              *float64 `json:"ph"`
	TotalAlkalinity *float64 `json:"total_alkalinity"`
	CalciumHardness *float64 `json:"calcium_hardness"`

	CyanuricAcid *float64 `json:"cyanuric_acid"`
	Phosphate    *float64 `json:"phosphate"`
	Borate       *float64 `json:"borate"`
	TDS          *float64 `json:"tds"`
	Temperature  *float64 `json:"temperature"`

	TotalCopper    *float64 `json:"total_copper"`
	FreeCopper     *float64 `json:"free_copper"`
	CombinedCopper *float64 `json:"combined_copper"`
	Iron           *float64 `json:"iron"`

	Notes string `json:"notes"`
}

func (req *testRequest) apply(t *store.Test) {
	t.FreeChlorine, t.TotalChlorine, t.CombinedChlorine = req.FreeChlorine, req.TotalChlorine, req.CombinedChlorine
	t.TotalSalt, t.Bromine = req.TotalSalt, req.Bromine
	t.PH, t.TotalAlkalinity, t.CalciumHardness = req.PH, req.TotalAlkalinity, req.CalciumHardness
	t.CyanuricAcid, t.Phosphate, t.Borate, t.TDS, t.Temperature = req.CyanuricAcid, req.Phosphate, req.Borate, req.TDS, req.Temperature
	t.TotalCopper, t.FreeCopper, t.CombinedCopper, t.Iron = req.TotalCopper, req.FreeCopper, req.CombinedCopper, req.Iron
	t.WQI = req.WQI
	if req.Operator != "" {
		t.Operator = req.Operator
	}
	if req.TestCount != nil {
		t.TestCount = req.TestCount
	}
	if req.Weather != "" {
		t.Weather = req.Weather
	}
}

// derive computes combined chlorine, adjusted alkalinity, the saturation index
// and the overall score, then rewrites the machine-generated treatments.
func (s *Server) derive(p *store.Pool, t *store.Test) error {
	// Combined chlorine is total minus free; fill it in when the lab did not.
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

	profile := p.Profile()
	readings := profile.Evaluate(t.Values())
	score := int64(chem.ScoreOf(readings, t.LSI))
	t.Score = &score
	return nil
}

// saveTreatments recomputes the recommended doses for a stored test.
func (s *Server) saveTreatments(p *store.Pool, t *store.Test) error {
	doses := p.Profile().Doses(t.Values())
	ts := make([]store.Treatment, 0, len(doses))
	for _, d := range doses {
		amount := d.Amount
		ts = append(ts, store.Treatment{
			Parameter: d.Parameter, Product: d.Product, Amount: &amount, Unit: d.Unit,
			Reason: d.Reason, Note: d.Note, Priority: int64(d.Priority),
		})
	}
	return s.DB.ReplaceComputedTreatments(t.ID, ts)
}

func (s *Server) handleCreateTest(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req testRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pool, err := s.DB.Pool(u.ID, req.PoolID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown pool_id")
		return
	}

	testedAt := strings.TrimSpace(req.TestedAt)
	if testedAt == "" {
		testedAt = store.Now()
	} else {
		normalised, err := normaliseTimestamp(testedAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		testedAt = normalised
	}

	t := &store.Test{
		PoolID: pool.ID, TestedAt: testedAt, Source: orDefault(req.Source, "manual"),
		CompanyID: req.CompanyID,
	}
	// A company can be named instead of referenced, so an API caller does not
	// need to look the id up first.
	if t.CompanyID == nil && strings.TrimSpace(req.CompanyName) != "" {
		c, err := s.DB.CompanyByName(u.ID, strings.TrimSpace(req.CompanyName), "store")
		if err == nil {
			t.CompanyID = &c.ID
		}
	}
	req.apply(t)
	if err := s.derive(pool, t); err != nil {
		writeStoreError(w, err)
		return
	}

	created, err := s.DB.CreateTest(t)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.saveTreatments(pool, created); err != nil {
		writeStoreError(w, err)
		return
	}
	if strings.TrimSpace(req.Notes) != "" {
		s.DB.CreateNote(&store.Note{TestID: &created.ID, PoolID: pool.ID, UserID: &u.ID, Kind: "human", Body: req.Notes})
	}

	writeJSON(w, http.StatusCreated, s.testDetail(pool, created))
}

func (s *Server) handleListTests(w http.ResponseWriter, r *http.Request) {
	f := store.TestFilter{
		PoolID: queryInt(r, "pool_id"),
		From:   r.URL.Query().Get("from"),
		To:     r.URL.Query().Get("to"),
		Limit:  int(queryInt(r, "limit")),
	}
	tests, err := s.DB.ListTests(userFrom(r).ID, f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tests)
}

func (s *Server) handleGetTest(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid test id")
		return
	}
	t, err := s.DB.Test(u.ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	pool, err := s.DB.Pool(u.ID, t.PoolID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.testDetail(pool, t))
}

// testDetail decorates a test with everything the UI needs to render it.
func (s *Server) testDetail(p *store.Pool, t *store.Test) map[string]any {
	profile := p.Profile()
	readings := profile.Evaluate(t.Values())
	alerts := profile.Alerts(t.Values(), t.LSI)
	treatments, _ := s.DB.ListTreatments(t.ID)
	notes, _ := s.DB.ListNotes(p.ID, &t.ID)
	attachments, _ := s.DB.ListReceipts(p.UserID, store.ReceiptFilter{PoolID: p.ID, TestID: t.ID})

	var verdict string
	var verdictStatus chem.Status
	if t.LSI != nil {
		verdictStatus, verdict = chem.LSIVerdict(*t.LSI)
	}
	return map[string]any{
		"test":        t,
		"pool":        p,
		"readings":    readings,
		"alerts":      alerts,
		"treatments":  treatments,
		"notes":       notes,
		"attachments": attachments,
		"lsi_verdict": verdict,
		"lsi_status":  verdictStatus,
	}
}

func (s *Server) handleUpdateTest(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid test id")
		return
	}
	t, err := s.DB.Test(u.ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	pool, err := s.DB.Pool(u.ID, t.PoolID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req testRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TestedAt != "" {
		normalised, err := normaliseTimestamp(req.TestedAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		t.TestedAt = normalised
	}
	if req.CompanyID != nil {
		t.CompanyID = req.CompanyID
	}
	req.apply(t)
	if err := s.derive(pool, t); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.DB.UpdateTest(t); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.saveTreatments(pool, t); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.testDetail(pool, t))
}

func (s *Server) handleDeleteTest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid test id")
		return
	}
	if err := s.DB.DeleteTest(userFrom(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleMarkTreatmentApplied(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid treatment id")
		return
	}
	if _, err := s.DB.TreatmentOwner(userFrom(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	var req struct {
		Applied bool `json:"applied"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.MarkTreatmentApplied(id, req.Applied); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "applied": req.Applied})
}

// ── Notes ────────────────────────────────────────────────────────────────

func (s *Server) handleListNotes(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	poolID := queryInt(r, "pool_id")
	if poolID == 0 {
		writeError(w, http.StatusBadRequest, "pool_id is required")
		return
	}
	if _, err := s.DB.Pool(u.ID, poolID); err != nil {
		writeStoreError(w, err)
		return
	}
	var testID *int64
	if id := queryInt(r, "test_id"); id > 0 {
		testID = &id
	}
	notes, err := s.DB.ListNotes(poolID, testID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notes)
}

func (s *Server) handleCreateNote(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req struct {
		PoolID int64  `json:"pool_id"`
		TestID *int64 `json:"test_id"`
		Body   string `json:"body"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeError(w, http.StatusBadRequest, "body is required")
		return
	}
	if _, err := s.DB.Pool(u.ID, req.PoolID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown pool_id")
		return
	}
	n, err := s.DB.CreateNote(&store.Note{PoolID: req.PoolID, TestID: req.TestID, UserID: &u.ID, Kind: "human", Body: req.Body})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) handleUpdateNote(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid note id")
		return
	}
	var req struct {
		PoolID int64  `json:"pool_id"`
		Body   string `json:"body"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.DB.Pool(u.ID, req.PoolID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown pool_id")
		return
	}
	if err := s.DB.UpdateNote(req.PoolID, id, req.Body); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid note id")
		return
	}
	poolID := queryInt(r, "pool_id")
	if _, err := s.DB.Pool(u.ID, poolID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown pool_id")
		return
	}
	if err := s.DB.DeleteNote(poolID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── Helpers ──────────────────────────────────────────────────────────────

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

// normaliseTimestamp accepts a date or a full timestamp and returns RFC3339.
func normaliseTimestamp(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("could not parse %q as a date; use YYYY-MM-DD or an RFC3339 timestamp", s)
}

// normaliseDate accepts a date in several forms and returns YYYY-MM-DD.
func normaliseDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05", "01/02/2006", "2006/01/02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("could not parse %q as a date; use YYYY-MM-DD", s)
}

var errUnknownPool = errors.New("unknown pool_id")

// ownedPool loads a pool and confirms the caller owns it.
func (s *Server) ownedPool(r *http.Request, poolID int64) (*store.Pool, error) {
	if poolID == 0 {
		return nil, errUnknownPool
	}
	p, err := s.DB.Pool(userFrom(r).ID, poolID)
	if err != nil {
		return nil, errUnknownPool
	}
	return p, nil
}
