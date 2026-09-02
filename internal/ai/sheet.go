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
 "values":{"field":number},"notes":"one short caveat about legibility","confidence":0.0-1.0}

The only permitted keys in "values" are:
  free_chlorine, total_chlorine, combined_chlorine, bromine        (ppm)
  total_salt                                                       (ppm)
  ph                                                               (no unit)
  total_alkalinity, calcium_hardness, cyanuric_acid, borate, tds   (ppm)
  phosphate                                                        (ppb)
  temperature                                                      (degrees Celsius)
  total_copper, free_copper, combined_copper, iron                 (ppm)
  wqi                                                              (the saturation / water quality index as printed)

Rules:
- Omit a key entirely when the sheet does not show that reading. Never guess a
  plausible value, and never carry one over from a different row.
- Convert to the units above and say so in "notes" when you do. Temperature
  printed in Fahrenheit becomes Celsius. Salt printed in g/L or ppt becomes ppm
  (1 g/L = 1000 ppm). Phosphate printed in ppm becomes ppb (multiply by 1000).
- Stabilizer, CYA, isocyanuric acid and conditioner all mean cyanuric_acid.
  Hardness or "CH" means calcium_hardness. "TA" means total_alkalinity. "FC"
  and "TC" mean free and total chlorine.
- A sheet often prints the reading, an ideal range, and a dose to add in
  adjacent columns. Read the measured value only — never the target, the ideal
  range, or the amount to add.
- If a number is cut off, blurred, or you are guessing at a digit, leave the
  key out and say which row you could not read in "notes".
- If the photo is not a water analysis at all, reply with an empty "values"
  object and say so in "notes".
- confidence reflects the legibility of the sheet as a whole.`

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
}
