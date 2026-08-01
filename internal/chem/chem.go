// Package chem implements pool-water chemistry: ideal ranges, the Langelier
// Saturation Index (the "WQI" printed on Pulse Hydrometrics reports), and
// dosing math for correcting an out-of-range reading.
//
// Units are metric throughout: volume in litres, concentrations in ppm (mg/L),
// temperature in Celsius, doses in grams (or millilitres for liquids).
package chem

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Sanitizer is how the pool is kept sanitary. It changes which readings matter
// and what the ideal ranges are.
type Sanitizer string

const (
	SanitizerSalt     Sanitizer = "salt"
	SanitizerChlorine Sanitizer = "chlorine"
	SanitizerBromine  Sanitizer = "bromine"
	SanitizerMineral  Sanitizer = "mineral"
)

// Surface affects the calcium hardness range: plaster/concrete leaches calcium
// and needs more of it in the water, vinyl and fibreglass need less.
type Surface string

const (
	SurfaceVinyl      Surface = "vinyl"
	SurfaceConcrete   Surface = "concrete"
	SurfaceFiberglass Surface = "fiberglass"
	SurfacePainted    Surface = "painted"
)

// Status is a reading's verdict against its ideal range.
type Status string

const (
	StatusGood    Status = "good"    // inside the ideal range
	StatusWarning Status = "warning" // outside ideal, inside acceptable
	StatusSerious Status = "serious" // outside acceptable
	StatusUnknown Status = "unknown" // not tested
)

// Range is the ideal/acceptable window for a single parameter.
type Range struct {
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Unit       string     `json:"unit"`
	Ideal      [2]float64 `json:"ideal"`
	Acceptable [2]float64 `json:"acceptable"`
	Target     float64    `json:"target"` // what dosing aims for
	Group      string     `json:"group"`  // sanitizer | balance | performance | metals
	// Unscored parameters are shown and trended but excluded from the water
	// quality score: water temperature and TDS describe the conditions the
	// chemistry operates in, they are not faults to be corrected.
	Unscored bool `json:"unscored,omitempty"`
}

// Reading is one measured parameter with its verdict.
type Reading struct {
	Key    string     `json:"key"`
	Label  string     `json:"label"`
	Unit   string     `json:"unit"`
	Value  *float64   `json:"value"`
	Status Status     `json:"status"`
	Ideal  [2]float64 `json:"ideal"`
	Target float64    `json:"target"`
	Group  string     `json:"group"`
	// Unscored mirrors Range.Unscored: shown and trended, but not counted
	// against the water quality score.
	Unscored bool `json:"unscored,omitempty"`
	// Deviation is how far outside the ideal range the value sits, in units.
	// Zero when the reading is good or untested.
	Deviation float64 `json:"deviation"`
}

// Profile describes the pool being tested — everything that changes the ideal
// ranges or the dosing math.
type Profile struct {
	VolumeL   float64
	Sanitizer Sanitizer
	Surface   Surface
	SaltPool  bool // salt-water generator installed
}

// Ranges returns the ideal windows for this pool. The values follow the
// Pool & Hot Tub Alliance / APSP standards, with the salt and stabilizer
// targets matched to the Pool Life SALT profile used on the sample report
// (salt 3200 ppm, CYA 40 ppm).
func (p Profile) Ranges() []Range {
	chRange := [2]float64{175, 275}
	chAccept := [2]float64{150, 400}
	chTarget := 225.0
	switch p.Surface {
	case SurfaceConcrete, SurfacePainted:
		chRange, chAccept, chTarget = [2]float64{200, 300}, [2]float64{175, 400}, 250
	case SurfaceVinyl, SurfaceFiberglass:
		chRange, chAccept, chTarget = [2]float64{150, 250}, [2]float64{125, 350}, 200
	}

	rs := []Range{
		{Key: "ph", Label: "pH", Unit: "", Ideal: [2]float64{7.4, 7.6}, Acceptable: [2]float64{7.2, 7.8}, Target: 7.5, Group: "balance"},
		{Key: "total_alkalinity", Label: "Total Alkalinity", Unit: "ppm", Ideal: [2]float64{80, 120}, Acceptable: [2]float64{60, 180}, Target: 100, Group: "balance"},
		{Key: "calcium_hardness", Label: "Calcium Hardness", Unit: "ppm", Ideal: chRange, Acceptable: chAccept, Target: chTarget, Group: "balance"},
		{Key: "cyanuric_acid", Label: "Stabilizer (CYA)", Unit: "ppm", Ideal: [2]float64{30, 50}, Acceptable: [2]float64{20, 80}, Target: 40, Group: "performance"},
		{Key: "phosphate", Label: "Phosphate", Unit: "ppb", Ideal: [2]float64{0, 100}, Acceptable: [2]float64{0, 300}, Target: 0, Group: "performance"},
		{Key: "tds", Label: "Total Dissolved Solids", Unit: "ppm", Ideal: [2]float64{0, 1500}, Acceptable: [2]float64{0, 2500}, Target: 0, Group: "performance", Unscored: true},
		{Key: "temperature", Label: "Temperature", Unit: "°C", Ideal: [2]float64{26, 30}, Acceptable: [2]float64{18, 35}, Target: 28, Group: "performance", Unscored: true},
		{Key: "total_copper", Label: "Total Copper", Unit: "ppm", Ideal: [2]float64{0, 0.2}, Acceptable: [2]float64{0, 0.3}, Target: 0, Group: "metals"},
		{Key: "iron", Label: "Iron", Unit: "ppm", Ideal: [2]float64{0, 0.1}, Acceptable: [2]float64{0, 0.2}, Target: 0, Group: "metals"},
		{Key: "combined_chlorine", Label: "Combined Chlorine", Unit: "ppm", Ideal: [2]float64{0, 0.2}, Acceptable: [2]float64{0, 0.5}, Target: 0, Group: "sanitizer"},
	}

	switch p.Sanitizer {
	case SanitizerBromine:
		rs = append(rs, Range{Key: "bromine", Label: "Bromine", Unit: "ppm", Ideal: [2]float64{3, 5}, Acceptable: [2]float64{2, 6}, Target: 4, Group: "sanitizer"})
	default:
		rs = append(rs, Range{Key: "free_chlorine", Label: "Free Chlorine", Unit: "ppm", Ideal: [2]float64{1, 3}, Acceptable: [2]float64{0.5, 5}, Target: 2, Group: "sanitizer"})
	}
	if p.SaltPool || p.Sanitizer == SanitizerSalt {
		// TDS for a salt pool is dominated by the salt itself, so the generic
		// TDS ceiling does not apply; replace it with a salt-aware one.
		for i := range rs {
			if rs[i].Key == "tds" {
				rs[i].Ideal = [2]float64{0, 5000}
				rs[i].Acceptable = [2]float64{0, 6000}
			}
		}
		rs = append(rs, Range{Key: "total_salt", Label: "Salt", Unit: "ppm", Ideal: [2]float64{2700, 3400}, Acceptable: [2]float64{2500, 4500}, Target: 3200, Group: "sanitizer"})
	}

	sort.Slice(rs, func(i, j int) bool {
		gi, gj := groupOrder(rs[i].Group), groupOrder(rs[j].Group)
		if gi != gj {
			return gi < gj
		}
		return rs[i].Key < rs[j].Key
	})
	return rs
}

func groupOrder(g string) int {
	switch g {
	case "sanitizer":
		return 0
	case "balance":
		return 1
	case "performance":
		return 2
	default:
		return 3
	}
}

// Evaluate scores a set of measured values against this pool's ideal ranges.
// values is keyed by Range.Key; a missing or nil entry yields StatusUnknown.
func (p Profile) Evaluate(values map[string]*float64) []Reading {
	out := make([]Reading, 0, len(p.Ranges()))
	for _, r := range p.Ranges() {
		rd := Reading{Key: r.Key, Label: r.Label, Unit: r.Unit, Ideal: r.Ideal, Target: r.Target, Group: r.Group, Unscored: r.Unscored, Status: StatusUnknown}
		if v, ok := values[r.Key]; ok && v != nil {
			rd.Value = v
			rd.Status, rd.Deviation = classify(*v, r)
		}
		out = append(out, rd)
	}
	return out
}

func classify(v float64, r Range) (Status, float64) {
	switch {
	case v >= r.Ideal[0] && v <= r.Ideal[1]:
		return StatusGood, 0
	case v < r.Ideal[0]:
		dev := r.Ideal[0] - v
		if v >= r.Acceptable[0] {
			return StatusWarning, dev
		}
		return StatusSerious, dev
	default:
		dev := v - r.Ideal[1]
		if v <= r.Acceptable[1] {
			return StatusWarning, dev
		}
		return StatusSerious, dev
	}
}

// ---------------------------------------------------------------------------
// Langelier Saturation Index
// ---------------------------------------------------------------------------

// LSIInput is the set of readings the saturation index needs.
type LSIInput struct {
	PH              float64
	TotalAlkalinity float64 // ppm as CaCO3
	CalciumHardness float64 // ppm as CaCO3
	TemperatureC    float64
	CyanuricAcid    *float64 // optional; corrects alkalinity
	TDS             *float64 // optional; refines the constant
}

// LSI returns the Langelier Saturation Index. This is the number Pulse
// Hydrometrics prints as "WQI": below -0.3 the water is corrosive and will
// etch surfaces and dissolve metal; above +0.3 it scales.
//
//	LSI = pH + TF + CF + AF - TDSF
//
// Cyanuric acid contributes to measured total alkalinity without buffering, so
// carbonate alkalinity = TA - (CYA / 3) — the "adjusted TA" on the report.
func LSI(in LSIInput) float64 {
	carbonate := in.TotalAlkalinity
	if in.CyanuricAcid != nil {
		carbonate -= *in.CyanuricAcid / 3.0
	}
	if carbonate < 1 {
		carbonate = 1
	}
	return round2(in.PH + tempFactor(in.TemperatureC) + calciumFactor(in.CalciumHardness) + alkalinityFactor(carbonate) - tdsFactor(in.TDS))
}

// AdjustedAlkalinity is total alkalinity corrected for the non-buffering
// portion contributed by cyanuric acid.
func AdjustedAlkalinity(ta float64, cya *float64) float64 {
	if cya == nil {
		return round2(ta)
	}
	return round2(ta - *cya/3.0)
}

// LSIVerdict turns an index into a human verdict.
func LSIVerdict(lsi float64) (Status, string) {
	switch {
	case lsi < -0.5:
		return StatusSerious, "Aggressively corrosive — actively etching surfaces and dissolving metal."
	case lsi < -0.3:
		return StatusWarning, "Corrosive — water is pulling calcium out of surfaces and equipment."
	case lsi <= 0.3:
		return StatusGood, "Balanced — water is neither corrosive nor scaling."
	case lsi <= 0.5:
		return StatusWarning, "Scaling — calcium is starting to deposit on surfaces and the heater."
	default:
		return StatusSerious, "Heavily scaling — expect cloudy water and scale on the heater core."
	}
}

// tempTable is the temperature factor from the standard saturation-index
// nomograph, in Celsius. Each row is the temperature at which the factor
// *reaches* that value, so the factor is stepped rather than interpolated —
// this is what water-test software (including the Pulse Hydrometrics report
// this implementation is calibrated against) prints.
var tempTable = [][2]float64{{0, 0.0}, {2.8, 0.1}, {7.8, 0.2}, {11.7, 0.3}, {15.6, 0.4}, {18.9, 0.5}, {24.4, 0.6}, {28.9, 0.7}, {34.4, 0.8}, {40.6, 0.9}}

func tempFactor(c float64) float64 { return step(tempTable, c) }

// calciumFactor and alkalinityFactor use the closed-form logarithms the index
// is actually defined by, rather than the nomograph's rounded rows.
func calciumFactor(ppm float64) float64 {
	if ppm < 1 {
		ppm = 1
	}
	return math.Log10(ppm) - 0.4
}

func alkalinityFactor(ppm float64) float64 {
	if ppm < 1 {
		ppm = 1
	}
	return math.Log10(ppm)
}

// step returns the factor for the highest row whose threshold x has reached.
func step(table [][2]float64, x float64) float64 {
	f := table[0][1]
	for _, row := range table {
		if x >= row[0] {
			f = row[1]
		} else {
			break
		}
	}
	return f
}

// tdsFactor is the constant subtracted from the sum. 12.1 is correct up to
// ~1000 ppm TDS; salt pools run far higher and need a larger constant.
func tdsFactor(tds *float64) float64 {
	if tds == nil {
		return 12.1
	}
	switch {
	case *tds < 1000:
		return 12.1
	case *tds < 2000:
		return 12.14
	case *tds < 3000:
		return 12.19
	case *tds < 4000:
		return 12.23
	case *tds < 5000:
		return 12.26
	default:
		return 12.35
	}
}

// lookup does piecewise-linear interpolation over a sorted (x, factor) table,
// clamping at both ends.
func lookup(table [][2]float64, x float64) float64 {
	if x <= table[0][0] {
		return table[0][1]
	}
	last := len(table) - 1
	if x >= table[last][0] {
		return table[last][1]
	}
	for i := 0; i < last; i++ {
		lo, hi := table[i], table[i+1]
		if x >= lo[0] && x <= hi[0] {
			t := (x - lo[0]) / (hi[0] - lo[0])
			return round2(lo[1] + t*(hi[1]-lo[1]))
		}
	}
	return table[last][1]
}

// ---------------------------------------------------------------------------
// Dosing
// ---------------------------------------------------------------------------

// Dose is a recommended chemical addition.
type Dose struct {
	Parameter string  `json:"parameter"`
	Product   string  `json:"product"`
	Amount    float64 `json:"amount"`
	Unit      string  `json:"unit"` // g | ml | kg | L
	Reason    string  `json:"reason"`
	Priority  int     `json:"priority"` // 1 = do first
	Note      string  `json:"note,omitempty"`
}

// Display renders the amount in the friendliest unit (kg/L above 1000).
func (d Dose) Display() string {
	switch {
	case d.Unit == "g" && d.Amount >= 1000:
		return fmt.Sprintf("%.2f kg", d.Amount/1000)
	case d.Unit == "ml" && d.Amount >= 1000:
		return fmt.Sprintf("%.2f L", d.Amount/1000)
	case d.Amount >= 100:
		return fmt.Sprintf("%.0f %s", d.Amount, d.Unit)
	default:
		return fmt.Sprintf("%.2f %s", d.Amount, d.Unit)
	}
}

// Product strengths, as mass fraction of the active ingredient. These are the
// standard commodity grades; a store's branded equivalent may differ, which is
// why every dose carries the product name it was computed for.
const (
	calHypoStrength  = 0.73 // calcium hypochlorite, 73% available chlorine
	dichlorStrength  = 0.56 // sodium dichloro-s-triazinetrione dihydrate
	sodaAshStrength  = 1.00
	bicarbStrength   = 1.00
	calChlorStrength = 0.77 // calcium chloride flake
	muriaticStrength = 0.315
)

// Doses computes the corrective chemistry for a set of readings, ordered so the
// operator can apply them safely: metals first (they must be sequestered before
// any oxidiser goes in), then balance, then sanitiser.
func (p Profile) Doses(values map[string]*float64) []Dose {
	var out []Dose
	m3 := p.VolumeL / 1000.0 // 1 ppm in 1 m³ = 1 gram of active ingredient
	get := func(k string) (float64, bool) {
		if v, ok := values[k]; ok && v != nil {
			return *v, true
		}
		return 0, false
	}
	byKey := map[string]Range{}
	for _, r := range p.Ranges() {
		byKey[r.Key] = r
	}

	// 1. Metals. Sequester before adding any oxidiser, or the water turns brown.
	if cu, ok := get("total_copper"); ok && cu > 0.2 {
		// Sequestrant dosed by volume, not by metal concentration: 1 L per
		// 75,000 L is the standard maintenance rate, doubled for >0.2 ppm.
		ml := p.VolumeL / 75.0
		if cu > 0.4 {
			ml *= 2
		}
		out = append(out, Dose{
			Parameter: "total_copper", Product: "Metal sequestrant", Amount: round2(ml), Unit: "ml", Priority: 1,
			Reason: fmt.Sprintf("Copper is %.2f ppm (ideal ≤0.20). Metals stain surfaces and tint the water.", cu),
			Note:   "Wait 24 hours before adding any large chemical dose, and hold off on shocking until the sequestrant has circulated.",
		})
	}

	// 2. pH must be in range before alkalinity or chlorine behave predictably.
	if ph, ok := get("ph"); ok {
		r := byKey["ph"]
		if ph < r.Ideal[0] {
			// Soda ash: ~14 g/m³ raises pH by ~0.2 at typical alkalinity.
			g := (r.Target - ph) / 0.2 * 14 * m3
			out = append(out, Dose{Parameter: "ph", Product: "Soda ash (sodium carbonate)", Amount: round2(g), Unit: "g", Priority: 2,
				Reason: fmt.Sprintf("pH is %.2f (ideal %.1f–%.1f). Low pH is corrosive and burns eyes.", ph, r.Ideal[0], r.Ideal[1])})
		} else if ph > r.Ideal[1] {
			ml := (ph - r.Target) / 0.2 * 25 * m3 / muriaticStrength * 0.315
			out = append(out, Dose{Parameter: "ph", Product: "Muriatic acid (31.45%)", Amount: round2(ml), Unit: "ml", Priority: 2,
				Reason: fmt.Sprintf("pH is %.2f (ideal %.1f–%.1f). High pH kills chlorine efficiency and clouds water.", ph, r.Ideal[0], r.Ideal[1])})
		}
	}

	// 3. Total alkalinity — the buffer that keeps pH stable.
	if ta, ok := get("total_alkalinity"); ok {
		r := byKey["total_alkalinity"]
		if ta < r.Ideal[0] {
			// Sodium bicarbonate: 1.68 g/m³ raises TA by 1 ppm.
			g := (r.Target - ta) * 1.68 * m3 / bicarbStrength
			out = append(out, Dose{Parameter: "total_alkalinity", Product: "Sodium bicarbonate (alkalinity increaser)", Amount: round2(g), Unit: "g", Priority: 3,
				Reason: fmt.Sprintf("Alkalinity is %.0f ppm (ideal %.0f–%.0f). Without buffer, pH swings on every rainfall.", ta, r.Ideal[0], r.Ideal[1])})
		} else if ta > r.Acceptable[1] {
			ml := (ta - r.Target) * 3.6 * m3
			out = append(out, Dose{Parameter: "total_alkalinity", Product: "Muriatic acid (31.45%)", Amount: round2(ml), Unit: "ml", Priority: 3,
				Reason: fmt.Sprintf("Alkalinity is %.0f ppm (ideal %.0f–%.0f). High alkalinity locks pH high and causes scaling.", ta, r.Ideal[0], r.Ideal[1]),
				Note:   "Add slowly over the deep end with the pump running, then re-test after 24 hours."})
		}
	}

	// 4. Calcium hardness — protects surfaces from aggressive water.
	if ch, ok := get("calcium_hardness"); ok {
		r := byKey["calcium_hardness"]
		if ch < r.Ideal[0] {
			g := (r.Target - ch) * m3 / calChlorStrength
			out = append(out, Dose{Parameter: "calcium_hardness", Product: "Calcium chloride (hardness increaser)", Amount: round2(g), Unit: "g", Priority: 4,
				Reason: fmt.Sprintf("Hardness is %.0f ppm (ideal %.0f–%.0f). Soft water leaches calcium from grout and plaster.", ch, r.Ideal[0], r.Ideal[1])})
		} else if ch > r.Acceptable[1] {
			out = append(out, Dose{Parameter: "calcium_hardness", Product: "Partial drain and refill", Amount: round2(p.VolumeL * (ch - r.Target) / ch), Unit: "L", Priority: 4,
				Reason: fmt.Sprintf("Hardness is %.0f ppm (ideal %.0f–%.0f). Calcium cannot be chemically removed — dilute it.", ch, r.Ideal[0], r.Ideal[1])})
		}
	}

	// 5. Stabilizer — sunscreen for chlorine.
	if cya, ok := get("cyanuric_acid"); ok && p.Sanitizer != SanitizerBromine {
		r := byKey["cyanuric_acid"]
		if cya < r.Ideal[0] {
			g := (r.Target - cya) * m3 // stabilizer is sold as ~100% cyanuric acid
			out = append(out, Dose{Parameter: "cyanuric_acid", Product: "Stabilizer (cyanuric acid)", Amount: round2(g), Unit: "g", Priority: 5,
				Reason: fmt.Sprintf("Stabilizer is %.0f ppm (ideal %.0f–%.0f). Unstabilised chlorine burns off in a few hours of sun.", cya, r.Ideal[0], r.Ideal[1]),
				Note:   "Add to the skimmer in a sock; it dissolves slowly. Do not backwash for 24 hours."})
		} else if cya > r.Acceptable[1] {
			out = append(out, Dose{Parameter: "cyanuric_acid", Product: "Partial drain and refill", Amount: round2(p.VolumeL * (cya - r.Target) / cya), Unit: "L", Priority: 5,
				Reason: fmt.Sprintf("Stabilizer is %.0f ppm (ideal %.0f–%.0f). Over-stabilised water locks chlorine up ('chlorine lock').", cya, r.Ideal[0], r.Ideal[1])})
		}
	}

	// 6. Salt for the generator.
	if p.SaltPool || p.Sanitizer == SanitizerSalt {
		if salt, ok := get("total_salt"); ok {
			r := byKey["total_salt"]
			if salt < r.Ideal[0] {
				g := (r.Target - salt) * m3
				out = append(out, Dose{Parameter: "total_salt", Product: "Pool salt", Amount: round2(g), Unit: "g", Priority: 6,
					Reason: fmt.Sprintf("Salt is %.0f ppm (target %.0f). Below range the chlorinator cannot produce sanitiser and will eventually fault.", salt, r.Target),
					Note:   "Brush across the floor to dissolve; allow 24 hours of circulation before re-testing."})
			} else if salt > r.Acceptable[1] {
				out = append(out, Dose{Parameter: "total_salt", Product: "Partial drain and refill", Amount: round2(p.VolumeL * (salt - r.Target) / salt), Unit: "L", Priority: 6,
					Reason: fmt.Sprintf("Salt is %.0f ppm (target %.0f). Excess salt corrodes rails, heaters and stonework.", salt, r.Target)})
			}
		}
	}

	// 7. Chloramines — breakpoint chlorination at 10x combined chlorine.
	if cc, ok := get("combined_chlorine"); ok && cc > 0.2 {
		fc, _ := get("free_chlorine")
		needed := cc*10 - fc
		if needed > 0 {
			g := needed * m3 / calHypoStrength
			out = append(out, Dose{Parameter: "combined_chlorine", Product: "Calcium hypochlorite shock (73%)", Amount: round2(g), Unit: "g", Priority: 7,
				Reason: fmt.Sprintf("Combined chlorine is %.2f ppm (ideal ≤0.20). Chloramines cause the chlorine smell, itchy skin and red eyes.", cc),
				Note:   fmt.Sprintf("Breakpoint requires reaching %.1f ppm free chlorine in one dose — a partial dose makes it worse. Or %.0f g dichlor (56%%), or a non-chlorine oxidiser for same-day swimming.", cc*10, needed*m3/dichlorStrength)})
		}
	} else if fc, ok := get("free_chlorine"); ok && p.Sanitizer != SanitizerBromine {
		r := byKey["free_chlorine"]
		if fc < r.Ideal[0] {
			g := (r.Target - fc) * m3 / calHypoStrength
			out = append(out, Dose{Parameter: "free_chlorine", Product: "Calcium hypochlorite (73%)", Amount: round2(g), Unit: "g", Priority: 7,
				Reason: fmt.Sprintf("Free chlorine is %.2f ppm (ideal %.1f–%.1f). The water is not sanitised.", fc, r.Ideal[0], r.Ideal[1])})
		}
	}

	// 8. Phosphate feeds algae and starves the chlorinator.
	if po4, ok := get("phosphate"); ok && po4 > 300 {
		out = append(out, Dose{Parameter: "phosphate", Product: "Phosphate remover", Amount: round2(p.VolumeL / 50.0), Unit: "ml", Priority: 8,
			Reason: fmt.Sprintf("Phosphate is %.0f ppb (ideal ≤100). Phosphate is algae food and reduces salt-cell output.", po4),
			Note:   "Expect the water to cloud for 24–48 hours; filter continuously and clean the filter afterwards."})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// ---------------------------------------------------------------------------
// Derived checks
// ---------------------------------------------------------------------------

// Alert is a derived warning that is not a simple out-of-range reading.
type Alert struct {
	Severity Status `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// Alerts returns cross-parameter findings — the things a single reading cannot
// tell you on its own.
func (p Profile) Alerts(values map[string]*float64, lsi *float64) []Alert {
	var out []Alert
	get := func(k string) (float64, bool) {
		if v, ok := values[k]; ok && v != nil {
			return *v, true
		}
		return 0, false
	}

	if lsi != nil {
		st, msg := LSIVerdict(*lsi)
		if st != StatusGood {
			out = append(out, Alert{Severity: st, Title: fmt.Sprintf("Saturation index %+.2f", *lsi), Detail: msg})
		}
	}

	fc, hasFC := get("free_chlorine")
	cya, hasCYA := get("cyanuric_acid")
	if hasFC && hasCYA && cya > 0 {
		// The chlorine/CYA ratio governs how much of the free chlorine is
		// actually active. Below 5% the water can test "sanitised" and still
		// grow algae.
		ratio := fc / cya * 100
		if ratio < 5 {
			out = append(out, Alert{Severity: StatusSerious, Title: "Chlorine locked by stabilizer",
				Detail: fmt.Sprintf("Free chlorine is only %.1f%% of stabilizer (%.2f ppm FC vs %.0f ppm CYA). Below 5%% the chlorine is chemically bound and algae can grow despite a passing chlorine reading. Hold FC near %.1f ppm for this stabilizer level.", ratio, fc, cya, cya*0.075)})
		}
	}

	tc, hasTC := get("total_chlorine")
	if hasFC && hasTC && tc < fc-0.05 {
		out = append(out, Alert{Severity: StatusWarning, Title: "Inconsistent chlorine readings",
			Detail: fmt.Sprintf("Total chlorine (%.2f) is below free chlorine (%.2f), which is not chemically possible. Re-run the test — reagent or sample error is likely.", tc, fc)})
	}

	if cc, ok := get("combined_chlorine"); ok && cc > 0.5 {
		out = append(out, Alert{Severity: StatusSerious, Title: "High chloramines",
			Detail: fmt.Sprintf("Combined chlorine %.2f ppm. This is spent chlorine bound to ammonia and organics — it is what people mistake for 'too much chlorine'. Shock to breakpoint.", cc)})
	}

	if ph, ok := get("ph"); ok {
		if ta, ok2 := get("total_alkalinity"); ok2 && ta < 60 && ph < 7.2 {
			out = append(out, Alert{Severity: StatusSerious, Title: "No pH buffer",
				Detail: fmt.Sprintf("Alkalinity %.0f ppm with pH %.2f. With this little buffer the pH will crash after any rain or bather load. Raise alkalinity before touching pH.", ta, ph)})
		}
	}

	if cu, ok := get("total_copper"); ok && cu > 0.2 {
		out = append(out, Alert{Severity: StatusWarning, Title: "Copper present",
			Detail: fmt.Sprintf("Copper %.2f ppm. Copper in the water usually means either an ioniser, a copper-based algaecide, or acidic water dissolving the heat exchanger. If the saturation index is negative, suspect the heater.", cu)})
	}

	if temp, ok := get("temperature"); ok && temp > 30 {
		out = append(out, Alert{Severity: StatusWarning, Title: "Warm water burns chlorine",
			Detail: fmt.Sprintf("Water is %.0f°C. Chlorine demand rises sharply above 30°C and algae doubles roughly every 24 hours — test more often while this lasts.", temp)})
	}
	return out
}

// ScoreOf collapses a set of readings into a 0–100 water-quality score, so a
// pool's condition can be trended as a single line.
func ScoreOf(readings []Reading, lsi *float64) int {
	score := 100.0
	counted := 0
	for _, r := range readings {
		if r.Status == StatusUnknown || r.Unscored {
			continue
		}
		counted++
		weight := 1.0
		switch r.Group {
		case "sanitizer":
			weight = 2.0
		case "balance":
			weight = 1.5
		}
		switch r.Status {
		case StatusWarning:
			score -= 5 * weight
		case StatusSerious:
			score -= 12 * weight
		}
	}
	if counted == 0 {
		return 0
	}
	if lsi != nil {
		if d := math.Abs(*lsi); d > 0.3 {
			score -= math.Min(25, (d-0.3)*40)
		}
	}
	return int(math.Round(math.Max(0, math.Min(100, score))))
}

// ParseSanitizer maps free-text from a report into a known sanitizer.
func ParseSanitizer(s string) Sanitizer {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "salt", "saltwater", "salt water", "swg":
		return SanitizerSalt
	case "bromine", "br":
		return SanitizerBromine
	case "mineral", "minerals":
		return SanitizerMineral
	default:
		return SanitizerChlorine
	}
}

// ParseSurface maps free-text from a report into a known surface.
func ParseSurface(s string) Surface {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "concrete", "plaster", "gunite", "pebble":
		return SurfaceConcrete
	case "fiberglass", "fibreglass":
		return SurfaceFiberglass
	case "painted", "paint":
		return SurfacePainted
	default:
		return SurfaceVinyl
	}
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
