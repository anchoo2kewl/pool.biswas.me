// Package ai generates water-chemistry insights through any OpenAI-compatible
// chat-completions endpoint. NVIDIA NIM, OpenRouter, OpenAI, Groq and a local
// Ollama all speak this dialect, so switching provider is a base-URL change
// rather than a code change.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// New builds a client. baseURL should include the /v1 suffix.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		// Reasoning-capable models on shared inference endpoints can take a
		// while; the request is made from a background job, not a page load.
		HTTP: &http.Client{Timeout: 120 * time.Second},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

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
}

const systemPrompt = `You are a pool water chemistry analyst writing for the pool's owner.

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

// Generate asks the model for an insight. The prompt is assembled by the
// caller so this package stays free of domain types.
func (c *Client) Generate(ctx context.Context, userPrompt string) (*Insight, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("no AI API key configured")
	}
	body, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   1600,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call model: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("decode model response (%s): %w", resp.Status, err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("model error: %s", cr.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model returned %s", resp.Status)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("model returned no choices")
	}

	content := cr.Choices[0].Message.Content
	insight, err := parseInsight(content)
	if err != nil {
		return nil, err
	}
	insight.Model = c.Model
	return insight, nil
}

// parseInsight tolerates the common ways a model wraps JSON: a markdown fence,
// leading prose, or a reasoning preamble.
func parseInsight(content string) (*Insight, error) {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("model did not return JSON: %s", truncate(content, 200))
	}
	s = s[start : end+1]

	var in Insight
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, fmt.Errorf("model returned malformed JSON: %w", err)
	}
	if in.Headline == "" && in.Summary == "" && len(in.Findings) == 0 {
		return nil, fmt.Errorf("model returned an empty analysis")
	}
	for i := range in.Findings {
		switch in.Findings[i].Severity {
		case "good", "warning", "serious":
		default:
			in.Findings[i].Severity = "warning"
		}
	}
	return &in, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
