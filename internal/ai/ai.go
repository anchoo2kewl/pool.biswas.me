// Package ai turns a go-ai provider chain into the two things this app asks a
// model for: an analysis of a water test, and the readings off a photographed
// test sheet.
//
// The prompts live here, the parsing is defensive, and every number a model
// returns is checked against what a pool can physically be before it reaches
// the database. A model is asked for JSON, not trusted to produce it.
package ai

import (
	"context"
	"fmt"
	"strings"

	goai "github.com/anchoo2kewl/go-ai"
)

// Service performs the app's AI calls against a provider chain.
//
// Vision gets its own chain because a provider's cheapest text model and its
// vision model are rarely the same one, and sending an image to a text-only
// model fails at the provider rather than degrading.
type Service struct {
	chain  *goai.Chain
	vision *goai.Chain
}

// New builds a Service. A nil vision chain falls back to the text chain, and a
// nil text chain reports Enabled() == false — so the app runs normally with
// the AI features simply switched off.
func New(chain, vision *goai.Chain) *Service {
	return &Service{chain: chain, vision: vision}
}

// FromSlots builds a Service from provider slots in priority order. Slots that
// are not configured are skipped, and an empty set is not an error: it means
// the feature is off.
func FromSlots(text, vision []goai.Slot) (*Service, error) {
	svc := &Service{}
	if len(text) > 0 {
		chain, err := goai.NewChainFromSlots(text...)
		if err != nil && err != goai.ErrNoProviders {
			return nil, err
		}
		svc.chain = chain
	}
	if len(vision) > 0 {
		chain, err := goai.NewChainFromSlots(vision...)
		if err != nil && err != goai.ErrNoProviders {
			return nil, err
		}
		svc.vision = chain
	}
	return svc, nil
}

// Enabled reports whether any provider is configured.
func (s *Service) Enabled() bool { return s != nil && s.chain != nil && s.chain.Len() > 0 }

// Providers lists the text chain, primary first.
func (s *Service) Providers() []string {
	if !s.Enabled() {
		return nil
	}
	return s.chain.Names()
}

// VisionProviders lists the chain a photo would be sent to.
func (s *Service) VisionProviders() []string {
	if !s.Enabled() {
		return nil
	}
	return s.visionChain().Names()
}

func (s *Service) visionChain() *goai.Chain {
	if s.vision != nil && s.vision.Len() > 0 {
		return s.vision
	}
	return s.chain
}

// ErrDisabled is returned when a feature is called with no provider set up.
var ErrDisabled = fmt.Errorf("ai: no AI provider configured")

// Finding is one observation from the model.
type Finding struct {
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"` // good | warning | serious
}

// Insight is the structured analysis returned for a test.
type Insight struct {
	Headline string    `json:"headline"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
	Actions  []string  `json:"actions"`
	Watch    []string  `json:"watch"`
	Model    string    `json:"model"`
	Provider string    `json:"provider,omitempty"`
}

const analysisPrompt = `You are a pool water chemistry analyst writing for the pool's owner.

You will be given a pool profile, its latest water test, the computed chemistry
(ideal ranges, saturation index, recommended doses), recent test history, and
recent spending on the pool.

Write an analysis that a non-chemist can act on today. Rules:
- Explain WHY a reading moved, using the history and the logbook. Connecting a
  change to something that was added to the pool is the most valuable thing you
  can do.
- Be specific and quantitative. Refer to actual numbers and dates.
- Never invent readings that were not provided. If something was not tested,
  say so rather than guessing.
- Do not repeat the dosing numbers verbatim; the interface already shows them.
  Comment on sequence, timing, and risk instead.
- The dose list is ordered for safety and is authoritative. Never recommend an
  order that contradicts it — in particular, metal sequestrant always goes in
  BEFORE any oxidiser or shock, never after, or the water turns brown.
- If spending looks unusual (repeated purchases of the same corrective), point
  out the underlying cause rather than the symptom.
- Keep each detail to two sentences at most.

Respond with JSON only, no markdown fence, matching exactly:
{
  "headline": "one short sentence, under 70 characters",
  "summary": "two or three sentences of plain-language overall assessment",
  "findings": [{"title": "short label", "detail": "what and why", "severity": "good|warning|serious"}],
  "actions": ["imperative step, most important first"],
  "watch": ["what to re-test and when"]
}`

// AnalysisPrompt returns the instructions the analysis is written under, so an
// agent writing one elsewhere is held to the same rules — including the dosing
// order, which is a safety rule rather than a stylistic one.
func AnalysisPrompt() string { return analysisPrompt }

// Analyse asks the model to interpret a test. The prompt is assembled by the
// caller, so this package stays free of the app's domain types.
func (s *Service) Analyse(ctx context.Context, userPrompt string) (*Insight, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	resp, err := s.chain.Complete(ctx, goai.Request{
		System:      analysisPrompt,
		Messages:    []goai.Message{goai.UserText(userPrompt)},
		Temperature: 0.3,
		// Room for a reasoning model to think and still answer; see the note
		// on the transcription request.
		MaxTokens: 8000,
		JSON:      true,
	})
	if err != nil {
		return nil, err
	}

	var in Insight
	if err := goai.ExtractJSON(resp.Text, &in); err != nil {
		return nil, fmt.Errorf("the model did not return a usable analysis: %w", err)
	}
	if err := in.Normalise(); err != nil {
		return nil, err
	}
	in.Model, in.Provider = resp.Model, resp.Provider
	return &in, nil
}

// Normalise tidies an analysis and rejects an empty one.
//
// It runs over an analysis written anywhere — by the chain above, or handed in
// by an agent that did the reading itself. An external author is not more
// trusted than a local one just because it arrived over HTTP, and the
// interface renders severities directly.
func (in *Insight) Normalise() error {
	in.Headline = trimTo(in.Headline, 200)
	in.Summary = trimTo(in.Summary, 2000)

	findings := in.Findings[:0]
	for _, f := range in.Findings {
		f.Title = trimTo(f.Title, 120)
		f.Detail = trimTo(f.Detail, 1200)
		if f.Title == "" && f.Detail == "" {
			continue
		}
		switch f.Severity {
		case "good", "warning", "serious":
		default:
			// An unrecognised severity is rendered as a warning rather than
			// dropped: the observation may still be worth reading.
			f.Severity = "warning"
		}
		findings = append(findings, f)
		if len(findings) == 20 {
			break
		}
	}
	in.Findings = findings
	in.Actions = trimList(in.Actions, 20, 500)
	in.Watch = trimList(in.Watch, 20, 500)

	if in.Headline == "" && in.Summary == "" && len(in.Findings) == 0 {
		return fmt.Errorf("the analysis is empty")
	}
	return nil
}

func trimList(in []string, max, width int) []string {
	out := in[:0]
	for _, v := range in {
		if v = trimTo(v, width); v != "" {
			out = append(out, v)
		}
		if len(out) == max {
			break
		}
	}
	return out
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
