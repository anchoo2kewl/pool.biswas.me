package ai

import (
	"strings"
	"testing"
)

// A model misreading 7.3 as 73 would otherwise produce a dose recommendation
// from a number no pool has ever had. There is no way to know which digit was
// wrong, so the reading is thrown away rather than corrected.
func TestNormaliseDropsImpossibleReadings(t *testing.T) {
	r := &SheetReading{Values: map[string]float64{
		"ph":                73,   // a misplaced decimal point
		"free_chlorine":     0.23, // fine
		"total_alkalinity":  106,  // fine
		"calcium_hardness":  -40,  // negative
		"cyanuric_acid":     5,    // fine
		"temperature":       451,  // Fahrenheit misread, or nonsense
		"chlorine_smell":    3,    // not a field this app stores
		"total_salt":        2161, // fine
		"COMBINED_CHLORINE": 0.26, // the right field, shouted
	}}
	r.normalise()

	for _, key := range []string{"free_chlorine", "total_alkalinity", "cyanuric_acid", "total_salt"} {
		if _, ok := r.Values[key]; !ok {
			t.Errorf("%s was dropped, but it is a perfectly ordinary reading", key)
		}
	}
	if v, ok := r.Values["combined_chlorine"]; !ok || v != 0.26 {
		t.Error("a key differing only in case was dropped")
	}
	for _, key := range []string{"ph", "calcium_hardness", "temperature", "chlorine_smell"} {
		if _, ok := r.Values[key]; ok {
			t.Errorf("%s = %v was stored, but it is not a value pool water can have", key, r.Values[key])
		}
	}

	// The discards have to be visible, or a blank row months later has no
	// explanation. An unknown key is not a discard — there was nothing to
	// store it in.
	joined := strings.Join(r.Rejected, " ")
	for _, want := range []string{"ph=73", "calcium_hardness=-40", "temperature=451"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Rejected = %v, want it to name %s", r.Rejected, want)
		}
	}
	if strings.Contains(joined, "chlorine_smell") {
		t.Error("an unrecognised field was reported as a rejected reading")
	}
}

func TestNormaliseBoundsTheStrings(t *testing.T) {
	r := &SheetReading{
		TestedAt:   "  2026-07-29T00:00:00Z ",
		Company:    "  Jameson Pool & Spa  ",
		Notes:      strings.Repeat("x", 900),
		Confidence: 4.5,
		Values:     map[string]float64{"ph": 7.3},
	}
	r.normalise()

	if r.TestedAt != "2026-07-29" {
		t.Errorf("TestedAt = %q, want the date part only", r.TestedAt)
	}
	if r.Company != "Jameson Pool & Spa" {
		t.Errorf("Company = %q, want it trimmed", r.Company)
	}
	if len(r.Notes) != 600 {
		t.Errorf("Notes is %d characters, want it bounded to 600", len(r.Notes))
	}
	if r.Confidence != 1 {
		t.Errorf("Confidence = %v, want it clamped into 0..1", r.Confidence)
	}
}

func TestEmptyReading(t *testing.T) {
	r := &SheetReading{Values: map[string]float64{"nonsense": 1}}
	r.normalise()
	if !r.Empty() {
		t.Error("a reading of nothing storable does not report itself as empty")
	}
}

// The prompt names the fields it is allowed to return, so the two lists have
// to stay in step — a field in the table but missing from the prompt is one
// the model will never fill in.
func TestPromptCoversEveryField(t *testing.T) {
	for _, field := range Fields() {
		if !strings.Contains(sheetPrompt, field) {
			t.Errorf("%s is a storable reading but the prompt never mentions it", field)
		}
	}
}

// A service with no providers must refuse rather than panic, since running
// with the AI features off is a supported configuration.
func TestDisabledServiceRefuses(t *testing.T) {
	svc := New(nil, nil)
	if svc.Enabled() {
		t.Fatal("a service with no chain reports itself as enabled")
	}
	if _, err := svc.ReadTestSheet(t.Context(), []byte("x"), "image/jpeg", ""); err != ErrDisabled {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
	if _, err := svc.Analyse(t.Context(), "prompt"); err != ErrDisabled {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
}

// An analysis written by an agent elsewhere gets no more trust than one this
// server generated: same validation, same bounds, same severity vocabulary.
func TestNormaliseGuardsASuppliedAnalysis(t *testing.T) {
	in := &Insight{
		Headline: "  Copper is high  ",
		Findings: []Finding{
			{Title: "Copper", Detail: "0.50 ppm", Severity: "catastrophic"},
			{Title: "", Detail: ""},
			{Title: "pH", Detail: "fine", Severity: "good"},
		},
		Actions: []string{"  Sequester first  ", "", "   "},
	}
	if err := in.Normalise(); err != nil {
		t.Fatalf("Normalise: %v", err)
	}
	if in.Headline != "Copper is high" {
		t.Errorf("headline = %q, want it trimmed", in.Headline)
	}
	if len(in.Findings) != 2 {
		t.Fatalf("findings = %d, want the empty one dropped", len(in.Findings))
	}
	// An unrecognised severity is rendered, so it has to land on a known one.
	if in.Findings[0].Severity != "warning" {
		t.Errorf("severity = %q, want an unknown one normalised to warning", in.Findings[0].Severity)
	}
	if in.Findings[1].Severity != "good" {
		t.Errorf("severity = %q, want a valid one kept", in.Findings[1].Severity)
	}
	if len(in.Actions) != 1 || in.Actions[0] != "Sequester first" {
		t.Errorf("actions = %v, want the blanks dropped and the rest trimmed", in.Actions)
	}

	if err := (&Insight{}).Normalise(); err == nil {
		t.Error("an empty analysis was accepted")
	}
}

// The instructions must travel with the context, or an agent writing the
// analysis elsewhere has no way to honour the rules the local path follows —
// the dosing order most of all.
func TestAnalysisPromptIsAvailableAndCarriesTheSafetyRule(t *testing.T) {
	p := AnalysisPrompt()
	if !strings.Contains(p, "sequestrant") {
		t.Error("the exported instructions do not mention the sequestrant ordering rule")
	}
	if !strings.Contains(p, "JSON") {
		t.Error("the exported instructions do not state the required shape")
	}
}
