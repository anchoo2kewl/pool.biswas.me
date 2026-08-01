package chem

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

// sampleProfile is the pool from the reference Pulse Hydrometrics report:
// 58,000 L vinyl on-ground salt pool.
var sampleProfile = Profile{VolumeL: 58000, Sanitizer: SanitizerSalt, Surface: SurfaceVinyl, SaltPool: true}

// sampleValues are the readings from that report (29 July 2026).
var sampleValues = map[string]*float64{
	"free_chlorine":     f(0.23),
	"total_chlorine":    f(0.49),
	"combined_chlorine": f(0.26),
	"total_salt":        f(2161),
	"ph":                f(7.30),
	"total_alkalinity":  f(106),
	"calcium_hardness":  f(159),
	"cyanuric_acid":     f(5),
	"phosphate":         f(0),
	"temperature":       f(21),
	"total_copper":      f(0.30),
	"iron":              f(0.10),
}

// The report prints an adjusted alkalinity of 104.33 from TA 106 / CYA 5.
func TestAdjustedAlkalinityMatchesReport(t *testing.T) {
	got := AdjustedAlkalinity(106, f(5))
	if math.Abs(got-104.33) > 0.01 {
		t.Fatalf("adjusted alkalinity = %.2f, want 104.33", got)
	}
}

// The report prints WQI -0.49 for these readings. Our index must land within
// the rounding tolerance of the nomograph it is interpolated from.
func TestLSIMatchesReportedWQI(t *testing.T) {
	got := LSI(LSIInput{
		PH:              7.30,
		TotalAlkalinity: 106,
		CalciumHardness: 159,
		TemperatureC:    21,
		CyanuricAcid:    f(5),
	})
	if math.Abs(got-(-0.49)) > 0.03 {
		t.Fatalf("LSI = %+.2f, want ≈ -0.49 (as printed on the report)", got)
	}
	st, _ := LSIVerdict(got)
	if st != StatusWarning && st != StatusSerious {
		t.Fatalf("LSI %+.2f should read as corrosive, got status %q", got, st)
	}
}

// The report recommends 60.262 kg of salt. Target 3200 ppm over 58,000 L from
// 2161 ppm is (3200-2161) g/m³ x 58 m³ = 60.262 kg.
func TestSaltDoseMatchesReport(t *testing.T) {
	var salt *Dose
	for _, d := range sampleProfile.Doses(sampleValues) {
		if d.Parameter == "total_salt" {
			d := d
			salt = &d
		}
	}
	if salt == nil {
		t.Fatal("no salt dose recommended for a 2161 ppm salt pool")
	}
	wantG := 60262.0
	if math.Abs(salt.Amount-wantG) > 1 {
		t.Fatalf("salt dose = %.0f g, want %.0f g (report says 60.262 kg)", salt.Amount, wantG)
	}
	if got := salt.Display(); got != "60.26 kg" {
		t.Fatalf("Display() = %q, want %q", got, "60.26 kg")
	}
}

// The report recommends 2.03 kg of stabilizer: target 40 ppm from 5 ppm over
// 58 m³ = 2.03 kg.
func TestStabilizerDoseMatchesReport(t *testing.T) {
	for _, d := range sampleProfile.Doses(sampleValues) {
		if d.Parameter == "cyanuric_acid" {
			if math.Abs(d.Amount-2030) > 1 {
				t.Fatalf("stabilizer dose = %.0f g, want 2030 g (report says 2.03 kg)", d.Amount)
			}
			return
		}
	}
	t.Fatal("no stabilizer dose recommended for a 5 ppm CYA pool")
}

// Metals must be sequestered before any oxidiser is added, so the copper dose
// has to sort ahead of the shock dose.
func TestMetalsDosedBeforeShock(t *testing.T) {
	doses := sampleProfile.Doses(sampleValues)
	copperAt, shockAt := -1, -1
	for i, d := range doses {
		switch d.Parameter {
		case "total_copper":
			copperAt = i
		case "combined_chlorine":
			shockAt = i
		}
	}
	if copperAt == -1 {
		t.Fatal("copper at 0.30 ppm should be treated")
	}
	if shockAt == -1 {
		t.Fatal("combined chlorine at 0.26 ppm should be shocked")
	}
	if copperAt > shockAt {
		t.Fatalf("copper dose (index %d) must come before shock (index %d)", copperAt, shockAt)
	}
}

func TestEvaluateFlagsTheSampleReport(t *testing.T) {
	readings := sampleProfile.Evaluate(sampleValues)
	want := map[string]Status{
		"free_chlorine":     StatusSerious, // 0.23 vs ideal 1-3, acceptable floor 0.5
		"combined_chlorine": StatusWarning, // 0.26 vs ideal <=0.2, acceptable <=0.5
		"total_salt":        StatusSerious, // 2161 vs acceptable floor 2500
		"ph":                StatusWarning, // 7.30 vs ideal 7.4-7.6
		"total_alkalinity":  StatusGood,    // 106 inside 80-120
		"cyanuric_acid":     StatusSerious, // 5 vs acceptable floor 20
		"total_copper":      StatusWarning, // 0.30 vs ideal <=0.2, acceptable <=0.3
	}
	got := map[string]Status{}
	for _, r := range readings {
		got[r.Key] = r.Status
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: status = %q, want %q", k, got[k], w)
		}
	}
}

// 5 ppm CYA with 0.23 ppm FC is a 4.6% ratio — under the 5% floor.
func TestChlorineLockAlert(t *testing.T) {
	lsi := LSI(LSIInput{PH: 7.30, TotalAlkalinity: 106, CalciumHardness: 159, TemperatureC: 21, CyanuricAcid: f(5)})
	alerts := sampleProfile.Alerts(sampleValues, &lsi)
	if len(alerts) == 0 {
		t.Fatal("expected alerts for the sample report")
	}
	var sawLock, sawLSI bool
	for _, a := range alerts {
		if a.Title == "Chlorine locked by stabilizer" {
			sawLock = true
		}
		if a.Severity == StatusWarning || a.Severity == StatusSerious {
			if len(a.Detail) == 0 {
				t.Errorf("alert %q has no detail", a.Title)
			}
		}
		if a.Title[:len("Saturation")] == "Saturation" {
			sawLSI = true
		}
	}
	if !sawLock {
		t.Error("expected a chlorine-lock alert at FC 0.23 / CYA 5")
	}
	if !sawLSI {
		t.Error("expected a saturation-index alert at LSI -0.49")
	}
}

func TestScoreDropsForBadWater(t *testing.T) {
	lsi := -0.49
	bad := ScoreOf(sampleProfile.Evaluate(sampleValues), &lsi)
	good := map[string]*float64{
		"free_chlorine": f(2), "combined_chlorine": f(0.1), "total_salt": f(3200),
		"ph": f(7.5), "total_alkalinity": f(100), "calcium_hardness": f(200),
		"cyanuric_acid": f(40), "temperature": f(28), "total_copper": f(0), "iron": f(0),
	}
	balanced := 0.0
	goodScore := ScoreOf(sampleProfile.Evaluate(good), &balanced)
	if goodScore != 100 {
		t.Errorf("perfectly balanced water scored %d, want 100", goodScore)
	}
	if bad >= goodScore {
		t.Errorf("sample report scored %d, should be well below the balanced score %d", bad, goodScore)
	}
	if bad > 60 {
		t.Errorf("sample report scored %d; water this far out of range should score low", bad)
	}
}

func TestSurfaceChangesCalciumTarget(t *testing.T) {
	vinyl := Profile{VolumeL: 50000, Surface: SurfaceVinyl}
	concrete := Profile{VolumeL: 50000, Surface: SurfaceConcrete}
	find := func(p Profile) Range {
		for _, r := range p.Ranges() {
			if r.Key == "calcium_hardness" {
				return r
			}
		}
		t.Fatal("no calcium range")
		return Range{}
	}
	if find(vinyl).Target >= find(concrete).Target {
		t.Error("concrete pools need a higher calcium target than vinyl")
	}
}

func TestBromineProfileHasNoChlorineRange(t *testing.T) {
	p := Profile{VolumeL: 5000, Sanitizer: SanitizerBromine}
	for _, r := range p.Ranges() {
		if r.Key == "free_chlorine" {
			t.Fatal("bromine pool should not carry a free-chlorine range")
		}
	}
}
