package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	goai "github.com/anchoo2kewl/go-ai"
	"github.com/biswas-dev/pool/internal/ai"
	"github.com/biswas-dev/pool/internal/chem"
	"github.com/biswas-dev/pool/internal/config"
	"github.com/biswas-dev/pool/internal/store"
)

// aiService resolves the provider chain for a request.
//
// A user's own credentials win over the server's, so usage is billed to
// whoever configured it. Their key is a single endpoint rather than a chain:
// somebody pasting a key into a settings box is configuring one provider, and
// silently failing over to the server's would spend the operator's money on a
// request the user asked to pay for themselves.
func (s *Server) aiService(u *store.User) (*ai.Service, error) {
	// An account that has configured its own providers uses only those. The
	// operator's key is a convenience for whoever runs the server, not an
	// allowance for everyone who signs up — and silently falling back to it
	// would spend somebody else's money on a request this user asked to pay
	// for themselves.
	slots, err := s.DB.AIChainSlots(u.ID)
	if err != nil {
		return nil, err
	}
	if len(slots) > 0 {
		text, vision := userSlots(slots)
		svc, err := ai.FromSlots(text, vision)
		if err != nil {
			return nil, err
		}
		if svc.Enabled() {
			return svc, nil
		}
		return nil, fmt.Errorf("your AI providers are configured but none of them has a key — check Settings")
	}

	// The single-endpoint settings this app shipped with, still honoured for
	// an account that set one up before the chain existed.
	if u.AIAPIKey != "" {
		slot := config.Slot(
			firstNonEmpty(u.AIBaseURL, s.Cfg.AIBaseURL),
			u.AIAPIKey,
			firstNonEmpty(u.AIModel, s.Cfg.AIModel),
		)
		vision := slot
		if s.Cfg.AIVisionModel != "" && u.AIModel == "" {
			vision.Model = s.Cfg.AIVisionModel
		}
		return ai.FromSlots([]goai.Slot{slot}, []goai.Slot{vision})
	}

	// The operator's providers serve everyone only when the operator says so.
	// Otherwise they are theirs alone, and everybody else brings a key.
	if s.AI.Enabled() && (s.Cfg.AISharedKey || u.Role == "admin") {
		return s.AI, nil
	}
	return nil, fmt.Errorf("no AI provider configured — add your own key under Settings → AI providers")
}

// userSlots splits a user's stored rungs into the two chains go-ai builds
// from, in slot order.
func userSlots(slots []store.AIProvider) (text, vision []goai.Slot) {
	for _, p := range slots {
		slot := goai.Slot{
			Provider: p.Provider,
			Model:    p.Model,
			APIKey:   p.APIKey,
			BaseURL:  p.BaseURL,
		}
		// A provider go-ai does not know needs an explicit endpoint, and one
		// it does know needs none — filling it in from the provider's own
		// table keeps a half-completed form working.
		if slot.BaseURL == "" && goai.BaseURLFor(p.Provider) == "" && p.Provider != "anthropic" {
			continue
		}
		if p.Kind == store.AIKindVision {
			vision = append(vision, slot)
		} else {
			text = append(text, slot)
		}
	}
	return text, vision
}

// handleGenerateInsight analyses a test with the LLM and stores the result as
// an AI note beside the human ones.
func (s *Server) handleGenerateInsight(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid test id")
		return
	}
	test, err := s.DB.Test(u.ID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	pool, err := s.DB.Pool(u.ID, test.PoolID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	svc, err := s.aiService(u)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}

	prompt, err := s.buildPrompt(u, pool, test)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// The request outlives the browser's patience on a slow endpoint, so give
	// it its own budget rather than inheriting a short one.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 150*time.Second)
	defer cancel()

	insight, err := svc.Analyse(ctx, prompt)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the model could not be reached: "+err.Error())
		return
	}

	note, err := s.saveInsight(u, pool, test, insight)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"note": note, "insight": insight})
}

// saveInsight files an analysis as an AI note beside the human ones, keeping
// the structured JSON alongside the rendered markdown so the interface can
// present either.
func (s *Server) saveInsight(u *store.User, pool *store.Pool, test *store.Test, in *ai.Insight) (*store.Note, error) {
	meta, _ := json.Marshal(in)
	return s.DB.CreateNote(&store.Note{
		TestID: &test.ID, PoolID: pool.ID, UserID: &u.ID, Kind: "ai",
		Body: renderInsight(in), Model: in.Model, Meta: string(meta),
	})
}

// buildPrompt assembles everything the model needs: the pool, the test, the
// computed chemistry, recent history, and recent spending.
func (s *Server) buildPrompt(u *store.User, pool *store.Pool, test *store.Test) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "POOL\n")
	fmt.Fprintf(&b, "Name: %s\nVolume: %.0f L\nType: %s\nSurface: %s\nSanitizer: %s\nLocation: %s\n",
		pool.Name, pool.VolumeL, pool.WaterType, pool.Surface, pool.Sanitizer, pool.Location)
	if pool.TreatmentProfile != "" {
		fmt.Fprintf(&b, "Treatment profile: %s\n", pool.TreatmentProfile)
	}

	profile := pool.Profile()
	readings := profile.Evaluate(test.Values())

	fmt.Fprintf(&b, "\nCURRENT TEST (%s", test.TestedAt)
	if test.CompanyName != "" {
		fmt.Fprintf(&b, ", tested by %s", test.CompanyName)
	}
	fmt.Fprintf(&b, ")\n")
	for _, rd := range readings {
		if rd.Value == nil {
			continue
		}
		fmt.Fprintf(&b, "- %s: %.2f %s (ideal %.2f–%.2f) status=%s\n",
			rd.Label, *rd.Value, rd.Unit, rd.Ideal[0], rd.Ideal[1], rd.Status)
	}
	var untested []string
	for _, rd := range readings {
		if rd.Value == nil {
			untested = append(untested, rd.Label)
		}
	}
	if len(untested) > 0 {
		fmt.Fprintf(&b, "Not tested: %s\n", strings.Join(untested, ", "))
	}
	if test.LSI != nil {
		status, verdict := chem.LSIVerdict(*test.LSI)
		fmt.Fprintf(&b, "Saturation index (LSI): %+.2f — %s (%s)\n", *test.LSI, status, verdict)
	}
	if test.Score != nil {
		fmt.Fprintf(&b, "Water quality score: %d/100\n", *test.Score)
	}

	if alerts := profile.Alerts(test.Values(), test.LSI); len(alerts) > 0 {
		fmt.Fprintf(&b, "\nDERIVED WARNINGS\n")
		for _, a := range alerts {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", a.Severity, a.Title, a.Detail)
		}
	}

	if doses := profile.Doses(test.Values()); len(doses) > 0 {
		fmt.Fprintf(&b, "\nRECOMMENDED DOSES (already shown to the user)\n")
		for _, d := range doses {
			fmt.Fprintf(&b, "- %s: %s of %s\n", d.Parameter, d.Display(), d.Product)
		}
	}

	// History: the previous few tests, oldest first, so trends are readable.
	history, err := s.DB.ListTests(u.ID, store.TestFilter{PoolID: pool.ID, Limit: 8})
	if err != nil {
		return "", err
	}
	sort.Slice(history, func(i, j int) bool { return history[i].TestedAt < history[j].TestedAt })
	if len(history) > 1 {
		fmt.Fprintf(&b, "\nHISTORY (oldest first, including the current test)\n")
		for _, h := range history {
			fmt.Fprintf(&b, "- %s:", h.TestedAt[:10])
			writeIf(&b, " FC=%.2f", h.FreeChlorine)
			writeIf(&b, " CC=%.2f", h.CombinedChlorine)
			writeIf(&b, " pH=%.2f", h.PH)
			writeIf(&b, " TA=%.0f", h.TotalAlkalinity)
			writeIf(&b, " CH=%.0f", h.CalciumHardness)
			writeIf(&b, " CYA=%.0f", h.CyanuricAcid)
			writeIf(&b, " salt=%.0f", h.TotalSalt)
			writeIf(&b, " Cu=%.2f", h.TotalCopper)
			writeIf(&b, " temp=%.0fC", h.Temperature)
			writeIf(&b, " LSI=%+.2f", h.LSI)
			fmt.Fprintf(&b, "\n")
		}
	}

	// The logbook is what makes causal explanation possible.
	entries, err := s.DB.ListLogEntries(u.ID, store.LogFilter{PoolID: pool.ID, Limit: 25})
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		sort.Slice(entries, func(i, j int) bool { return entries[i].OccurredOn < entries[j].OccurredOn })
		fmt.Fprintf(&b, "\nRECENT LOGBOOK (what was added to or done for the pool)\n")
		var total int64
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s [%s] %s", e.OccurredOn, e.Category, e.Item)
			if e.Quantity != nil && *e.Quantity > 0 {
				fmt.Fprintf(&b, " %.2f %s", *e.Quantity, e.Unit)
			}
			if e.CostCents > 0 {
				fmt.Fprintf(&b, " (%s %.2f)", e.Currency, e.Cost())
			}
			if e.Notes != "" {
				fmt.Fprintf(&b, " — %s", e.Notes)
			}
			fmt.Fprintf(&b, "\n")
			total += e.CostCents
		}
		fmt.Fprintf(&b, "Spend across these entries: %.2f\n", float64(total)/100)
	}

	// Prior human notes carry context the readings cannot.
	notes, err := s.DB.ListNotes(pool.ID, nil)
	if err == nil {
		var human []store.Note
		for _, n := range notes {
			if n.Kind == "human" {
				human = append(human, n)
			}
		}
		if len(human) > 0 {
			fmt.Fprintf(&b, "\nOWNER NOTES\n")
			for i, n := range human {
				if i >= 5 {
					break
				}
				fmt.Fprintf(&b, "- %s: %s\n", n.CreatedAt[:10], n.Body)
			}
		}
	}
	return b.String(), nil
}

func writeIf(b *strings.Builder, format string, v *float64) {
	if v != nil {
		fmt.Fprintf(b, format, *v)
	}
}

// renderInsight turns the structured analysis into readable markdown for the
// note body; the JSON is kept alongside it in meta.
func renderInsight(in *ai.Insight) string {
	var b strings.Builder
	if in.Headline != "" {
		fmt.Fprintf(&b, "**%s**\n\n", in.Headline)
	}
	if in.Summary != "" {
		fmt.Fprintf(&b, "%s\n", in.Summary)
	}
	if len(in.Findings) > 0 {
		fmt.Fprintf(&b, "\n**Findings**\n")
		for _, f := range in.Findings {
			fmt.Fprintf(&b, "- [%s] %s — %s\n", f.Severity, f.Title, f.Detail)
		}
	}
	if len(in.Actions) > 0 {
		fmt.Fprintf(&b, "\n**Do now**\n")
		for i, a := range in.Actions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, a)
		}
	}
	if len(in.Watch) > 0 {
		fmt.Fprintf(&b, "\n**Watch**\n")
		for _, wtc := range in.Watch {
			fmt.Fprintf(&b, "- %s\n", wtc)
		}
	}
	return strings.TrimSpace(b.String())
}

var _ = http.StatusOK
