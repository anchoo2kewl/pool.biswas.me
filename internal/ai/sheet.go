package ai

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	goai "github.com/anchoo2kewl/go-ai"
)

// SheetReading is a water test transcribed from a photograph.
type SheetReading struct {
	// TestedAt is the date printed on the sheet, YYYY-MM-DD, or empty when
	// none was legible.
	TestedAt string `json:"tested_at"`
	// Company is whoever performed the test, as printed.
	Company string `json:"company"`
	// Operator is the named tester, if the sheet carries one.
	Operator string `json:"operator"`
	// TestCount is the sequence number the store prints on the sheet, so a
	// season's tests can be tied back to the store's own numbering.
	TestCount *int64 `json:"test_count"`
	// Pool is the profile printed at the top of the sheet. It describes the
	// body of water rather than the test, and is offered to the owner as an
	// update rather than applied: a volume read off a photograph must not
	// silently rewrite what every dose is calculated from.
	Pool *PoolProfile `json:"pool,omitempty"`
	// Values holds the readings, keyed by this app's field names. A key is
	// present only when the model actually read a number for it.
	Values map[string]float64 `json:"values"`
	// Notes is the model's own caveat about the photo — a blurred row, a
	// cropped column.
	Notes string `json:"notes"`
	// Confidence is the model's self-assessment, 0 to 1. It is advisory: it
	// decides what the interface warns about, never what is stored.
	Confidence float64 `json:"confidence"`
	// Rejected lists readings that were discarded as physically impossible,
	// with the value that was thrown away, so a misread digit is visible
	// rather than silent.
	Rejected []string `json:"rejected,omitempty"`

	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// PoolProfile is the pool description printed on a test sheet.
type PoolProfile struct {
	CustomerName     string   `json:"customer_name,omitempty"`
	SiteAddress      string   `json:"site_address,omitempty"`
	VolumeL          *float64 `json:"volume_l,omitempty"`
	WaterType        string   `json:"water_type,omitempty"`
	TreatmentProfile string   `json:"treatment_profile,omitempty"`
	Grade            string   `json:"grade,omitempty"`
	Surface          string   `json:"surface,omitempty"`
	Sanitizer        string   `json:"sanitizer,omitempty"`
	Location         string   `json:"location,omitempty"`
}

// Empty reports whether the profile carries nothing worth offering.
func (p *PoolProfile) Empty() bool {
	return p == nil || (p.CustomerName == "" && p.SiteAddress == "" && p.VolumeL == nil &&
		p.WaterType == "" && p.TreatmentProfile == "" && p.Grade == "" &&
		p.Surface == "" && p.Sanitizer == "" && p.Location == "")
}

// normalise tidies the profile and drops a volume no pool could have.
func (p *PoolProfile) normalise() {
	if p == nil {
		return
	}
	p.CustomerName = trimTo(p.CustomerName, 120)
	p.SiteAddress = trimTo(p.SiteAddress, 200)
	p.WaterType = trimTo(p.WaterType, 60)
	p.TreatmentProfile = trimTo(p.TreatmentProfile, 120)
	p.Grade = trimTo(p.Grade, 60)
	p.Surface = strings.ToLower(trimTo(p.Surface, 40))
	p.Sanitizer = strings.ToLower(trimTo(p.Sanitizer, 40))
	p.Location = trimTo(p.Location, 60)
	// A litre figure outside this range is a misread rather than a pool —
	// and it is the number every dose is calculated from.
	if p.VolumeL != nil && (*p.VolumeL < 500 || *p.VolumeL > 5_000_000) {
		p.VolumeL = nil
	}
}

// Empty reports whether nothing usable was read.
func (s *SheetReading) Empty() bool { return len(s.Values) == 0 }

// plausible is the range each reading must fall in to be stored.
//
// These are not "ideal" ranges — a pool can legitimately be far outside those,
// and that is exactly when someone photographs the sheet. They are the bounds
// of what water can physically read, so a misplaced decimal point or a digit
// the model invented is caught before it becomes a dose recommendation.
var plausible = map[string][2]float64{
	"free_chlorine":     {0, 50},
	"total_chlorine":    {0, 50},
	"combined_chlorine": {0, 20},
	"bromine":           {0, 80},
	"total_salt":        {0, 20000},
	"ph":                {4, 11},
	"total_alkalinity":  {0, 800},
	"calcium_hardness":  {0, 2000},
	"cyanuric_acid":     {0, 400},
	"phosphate":         {0, 20000},
	"borate":            {0, 300},
	"tds":               {0, 20000},
	"temperature":       {-5, 45},
	"total_copper":      {0, 10},
	"free_copper":       {0, 10},
	"combined_copper":   {0, 10},
	"iron":              {0, 10},
	"wqi":               {-5, 5},
}

// Fields lists the reading keys a sheet may carry, in the order the prompt
// presents them.
func Fields() []string {
	out := make([]string, 0, len(plausible))
	for k := range plausible {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const sheetPrompt = `You transcribe a swimming pool water analysis from a photograph. You are an OCR
step with a chemistry vocabulary, not an analyst: read what is printed, nothing more.

Reply with a single JSON object and nothing else:
{"tested_at":"YYYY-MM-DD or empty","company":"who performed the test","operator":"named tester or empty",
 "test_count":number or null,"values":{"field":number},
 "pool":{"customer_name":"","site_address":"","volume_l":number or null,"water_type":"","treatment_profile":"",
         "grade":"","surface":"","sanitizer":"","location":""},
 "notes":"one short caveat about legibility","confidence":0.0-1.0}

The only permitted keys in "values" are:
  free_chlorine, total_chlorine, combined_chlorine, bromine        (ppm)
  total_salt                                                       (ppm)
  ph                                                               (no unit)
  total_alkalinity, calcium_hardness, cyanuric_acid, borate, tds   (ppm)
  phosphate                                                        (ppb)
  temperature                                                      (degrees Celsius)
  total_copper, free_copper, combined_copper, iron                 (ppm)
  wqi                                                              (the water quality / saturation index as printed)

Rules:
- "N/T", "NT", "N/A", "--" and a blank cell all mean NOT TESTED. Omit the key
  entirely. Never write 0 for one: zero is a reading, and the difference
  between "no stabilizer in the water" and "nobody measured it" changes the
  advice completely.
- Omit a key when the sheet does not show that reading. Never guess a plausible
  value, and never carry one over from a different row.
- Convert to the units above and say so in "notes" when you do. Fahrenheit
  becomes Celsius. Salt in g/L or ppt becomes ppm (1 g/L = 1000 ppm).
- Stabilizer, CYA, conditioner and isocyanuric acid all mean cyanuric_acid.
  CH means calcium_hardness, TA means total_alkalinity, FCl/TCl/CCl mean free,
  total and combined chlorine, PO4 is phosphate, B is borate, TCu/FCu/CCu are
  the coppers, Fe is iron, Br is bromine, Temp is temperature.
- "Total Alkalinity Adjusted" (aTA) is a DERIVED figure. Ignore it — it is
  recomputed from the raw alkalinity. Only ever report the plain TA as
  total_alkalinity.
- A sheet often prints the reading, an ideal range, and a dose to add in
  adjacent columns, and a TREATMENT section underneath naming quantities of
  product. Read the measured value only. Never read a target, an ideal range,
  or an amount to add — "add 2.03kg Pool Life Stab" is not a stabilizer
  reading.
- Handwriting is the owner's own annotation, not part of the report. Ignore it
  entirely, however emphatically it is circled.
- If a number is cut off, blurred, or you are guessing at a digit, leave the
  key out and say which row you could not read in "notes".
- If the photo is not a water analysis at all, reply with an empty "values"
  object and say so in "notes".
- confidence reflects the legibility of the sheet as a whole.

LAYOUT. These reports commonly print four labelled blocks side by side —
SANITIZER, BALANCE, PERFORMANCE, METALS — each followed by its own RESULT
column. Every reading's number sits in the RESULT column immediately to the
RIGHT of its own label. Read each block as its own two-column table. A value
belonging to the block to the left or right is the single most common way to
get this wrong, so check that each number you report is on the same line as,
and just after, the label you are assigning it to. A photograph taken at an
angle will make the columns look sheared; follow the printed rules and the
line the label sits on, not the apparent vertical alignment.

OTHER FIELDS.
- "tested_at" is the date printed at the top, often with an ordinal suffix and
  a weekday: "Wednesday, July 29th 2026" is 2026-07-29.
- "company" is the store or contractor named on the sheet — the logo at the
  top, or the "Store Name" line at the bottom.
- "operator" and "test_count" are usually printed at the bottom left.
- "wqi" is the water quality index: on these reports it is the small number in
  the circle on the WQI gauge, and it is usually negative, between -1 and +1.
  It is not a percentage and not the score out of 100.
- "pool" describes the body of water, printed in the block at the top left:
  customer, site address, water volume (in litres), type, treatment profile,
  grade, surface, sanitizer and location. Copy them as printed and leave any
  you cannot see as an empty string or null. This describes the pool, not the
  test, and is used only to offer the owner an update — so never invent it.`

// ReadTestSheet transcribes the readings from a photograph of a test sheet.
//
// hint is anything the person typed alongside the photo — the testing company,
// or "the salt row is smudged" — and may be empty.
func (s *Service) ReadTestSheet(ctx context.Context, image []byte, mediaType, hint string) (*SheetReading, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if len(image) == 0 {
		return nil, fmt.Errorf("ai: no image to read")
	}

	prompt := "Transcribe the water test in this photo."
	if h := strings.TrimSpace(hint); h != "" {
		prompt += " The person adds: " + trimTo(h, 400)
	}
	if mediaType == "" {
		mediaType = "image/jpeg"
	}

	resp, err := s.visionChain().Complete(ctx, goai.Request{
		System:   sheetPrompt,
		Messages: []goai.Message{goai.UserImage(prompt, mediaType, image)},
		// Transcription is the one task where creativity is purely a defect.
		Temperature: 0,
		MaxTokens:   1600,
		JSON:        true,
	})
	if err != nil {
		return nil, err
	}

	var read SheetReading
	if err := goai.ExtractJSON(resp.Text, &read); err != nil {
		return nil, fmt.Errorf("the model did not return a usable transcription: %w", err)
	}
	read.Model, read.Provider = resp.Model, resp.Provider
	read.normalise()

	if read.Empty() {
		detail := strings.TrimSpace(read.Notes)
		if detail == "" {
			detail = "no readings were legible"
		}
		return &read, fmt.Errorf("no readings could be read from that photo: %s", detail)
	}
	return &read, nil
}

// normalise drops what cannot be stored and tidies what can.
//
// A vision model that misreads 7.3 as 73 produces a number the chemistry
// engine would happily turn into a dose recommendation, so an implausible
// value is thrown away here rather than corrected — there is no way to know
// which digit was wrong.
func (r *SheetReading) normalise() {
	clean := make(map[string]float64, len(r.Values))
	for key, v := range r.Values {
		key = strings.ToLower(strings.TrimSpace(key))
		bounds, known := plausible[key]
		if !known {
			continue
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < bounds[0] || v > bounds[1] {
			r.Rejected = append(r.Rejected, fmt.Sprintf("%s=%g", key, v))
			continue
		}
		clean[key] = v
	}
	sort.Strings(r.Rejected)
	r.Values = clean

	r.TestedAt = trimTo(r.TestedAt, 10)
	r.Company = trimTo(r.Company, 120)
	r.Operator = trimTo(r.Operator, 120)
	r.Notes = trimTo(r.Notes, 600)
	r.Confidence = math.Min(1, math.Max(0, r.Confidence))

	if r.TestCount != nil && (*r.TestCount < 0 || *r.TestCount > 100000) {
		r.TestCount = nil
	}
	r.Pool.normalise()
	if r.Pool.Empty() {
		r.Pool = nil
	}
}
