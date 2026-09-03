# pool.biswas.me

Pool water chemistry and cost-of-ownership tracking. Log every water test and
every dollar spent on the pool — chemicals, filters, salt cells, service calls —
and see what a season actually costs, alongside a treatment plan computed from
your own water.

One Go binary, one embedded database, one ~74 MB container.

## What it does

**Water chemistry.** Records the full panel a pool store prints — free/total/
combined chlorine, pH, alkalinity, calcium hardness, stabilizer, salt, phosphate,
TDS, metals — and scores each reading against the correct range for your pool's
surface and sanitizer. It computes the Langelier Saturation Index (the "WQI" on a
Pulse Hydrometrics report) and reproduces the printed value to within 0.01, plus
adjusted alkalinity and a 0–100 water quality score.

**Treatment plans.** Exact doses for your pool's volume, ordered so they are safe
to apply in sequence: sequester metals before adding any oxidiser, fix alkalinity
before chasing pH. Each dose explains why it is needed.

**Cross-parameter warnings.** Things a single reading cannot tell you — chlorine
locked by stabilizer below the 5% ratio, chloramines above breakpoint, a
collapsed pH buffer, copper that implies a corroding heat exchanger.

**Season logbook and costs.** Everything that goes into or onto the pool, with
quantity, cost, vendor and receipt. Backdating is the normal path — entries file
themselves into whichever season contains their date. Spend rolls up by season,
month, category, item and vendor, with a running total and season-over-season
comparison.

**A test from a photograph.** Photograph the printout the pool store hands over
and the whole of it — twenty readings, the date, who tested it — becomes a
scored test with a dosing plan, without typing a number. The photo is filed
against the test, so the readings can always be checked against the paper they
came from, and the analysis runs in the same request. A sheet already uploaded
as an attachment can be read the same way, so one filed before any of this
existed still becomes a scored test.

Readings that are physically impossible for pool water — a pH of 73, a negative
hardness — are discarded rather than corrected: there is no way to know which
digit was misread, and a wrong number here becomes a dose recommendation.
Whatever the model could not read is filed as a note, so a blank row still has
an explanation months later.

**Bring your own model.** Each account configures its own providers, as two
ordered chains — one for the analysis, one for reading a photographed sheet,
because a provider's cheapest text model and its vision model are rarely the
same one. Up to three slots each: slot 1 is tried first and the rest are
fallbacks. DeepSeek reports what is left on the key; NVIDIA NIM is free with a
personal key. The operator's own key stays the operator's unless
`POOL_AI_SHARED=true`.

**AI analysis.** Optional. Any OpenAI-compatible endpoint (NVIDIA NIM,
OpenRouter, OpenAI, Anthropic, a local Ollama), with a fallback chain: a
provider that is rate-limiting or down moves to the next one. It reads the test history *and* the
logbook, so it explains why a number moved rather than restating it —
connecting a chlorine crash to the stabilizer that washed out, or repeated salt
purchases to a suspected leak.

**An API for everything.** Every action in the interface is an API call, with a
personal key carrying a `read` or `read,write` scope that is enforced on the
method. The OpenAPI document is served at `/api/openapi.yaml`; see `/docs` on a
running instance.

## Demo

A public demo account is seeded on startup and rebuilt every two hours, so
visitors can change anything without spoiling it for the next person:

- **<https://pool.biswas.me>** — "Explore the live demo" signs straight in
- Credentials, if you want them: `demo@pool.biswas.me` / `poolside`

Disable it with `POOL_DEMO=false`.

## Running it

```bash
docker run -d --name pool \
  -p 8080:8080 \
  -v pool-data:/data \
  -e POOL_APP_URL=http://localhost:8080 \
  -e POOL_ADMIN_EMAIL=you@example.com \
  -e POOL_ADMIN_PASSWORD=change-this \
  ghcr.io/anchoo2kewl/pool.biswas.me:latest
```

Or from source:

```bash
go run ./cmd/server      # http://localhost:8080
go test ./...
```

Reading a test sheet needs a vision-capable model. Without one configured the
photo endpoint answers `412`, and everything else works exactly as before.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `POOL_ADDR` | `:8080` | Listen address |
| `POOL_DB_PATH` | `./data/pool.db` | Database file |
| `POOL_DATA_DIR` | `./data` | Uploads and attachments |
| `POOL_APP_URL` | `http://localhost:8080` | Public URL; OAuth callbacks derive from it |
| `POOL_REGISTRATION` | `open` | `open`, `invite`, or `closed` |
| `POOL_ADMIN_EMAIL` / `POOL_ADMIN_PASSWORD` | — | Seeds the first account on an empty database |
| `POOL_DEMO` | `true` | Seed and serve the public demo account |
| `POOL_DEMO_EMAIL` / `POOL_DEMO_PASSWORD` | `demo@pool.biswas.me` / `poolside` | Demo credentials, shown publicly on the sign-in page |
| `POOL_DEMO_RESET_HOURS` | `2` | How often the demo data is rebuilt |
| `POOL_AI_BASE_URL` | NVIDIA NIM | Any OpenAI-compatible endpoint |
| `POOL_AI_API_KEY` | — | Fallback key; users can set their own |
| `POOL_AI_MODEL` | `meta/llama-3.2-90b-vision-instruct` | Default model. Vision-capable, because reading a photographed test sheet needs it |
| `POOL_AI_VISION_MODEL` | — | Model for reading a photographed test sheet; falls back to `POOL_AI_MODEL` |
| `POOL_AI_SHARED` | `false` | Whether the keys above serve every account, or only the admin. Off, because a model key costs its owner money per request |
| `AI_1_*` / `AIV_1_*` | — | A go-ai fallback chain for text and vision. Takes precedence over `POOL_AI_*` |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | — | Enables Google sign-in |
| `GH_CLIENT_ID` / `GH_CLIENT_SECRET` | — | Enables GitHub sign-in |
| `OAUTH_STATE_SECRET` | random | Set a stable value, or sign-ins break across restarts |

Each OAuth provider appears on the sign-in page only when both its ID and secret
are set. Callback URLs are `${POOL_APP_URL}/auth/google/callback` and
`${POOL_APP_URL}/auth/github/callback`.

Note that a GitHub **OAuth App** permits exactly one callback URL, so this needs
its own app rather than sharing one with another site. Google allows several
redirect URIs on one client.

## How it is built

- **Go 1.26.5**, standard library HTTP routing, no web framework.
- **Four shared libraries**, the same ones behind the other apps here:
  [`go-ai`](https://github.com/anchoo2kewl/go-ai) for provider-agnostic model
  calls with an ordered fallback chain,
  [`go-api`](https://github.com/anchoo2kewl/go-api) for personal API tokens and
  OpenAPI discovery, and
  [`go-photo`](https://github.com/anchoo2kewl/go-photo) for image ingest —
  sniffing, downscaling, re-encoding and storing an upload where a crafted
  filename cannot reach.
- **[Turso](https://github.com/tursodatabase/turso)** embedded via
  [`turso-go`](https://github.com/tursodatabase/turso-go), which loads a Rust
  engine through `purego` — so there is no CGO, no database server, and no
  sidecar container.
- **No frontend build step.** The interface is plain ES modules and hand-rolled
  SVG charts, embedded into the binary with `go:embed`. There is no npm.

### Notes for anyone extending this

Two things about the embedded engine are worth knowing before you hit them:

1. **No window functions.** `lag`, `lead` and friends are unsupported, so running
   totals and deltas are computed in Go after the query (see
   `handleAnalyticsCosts`).
2. **Stricter nullability than SQLite.** A `UNIQUE` column is treated as
   non-nullable. Optional text columns are declared `NOT NULL DEFAULT ''`.

And two about the container:

3. **The binary embeds every platform's engine** — roughly 150 MB, of which one
   image needs one. The Dockerfile vendors the module and deletes the rest,
   taking the binary from ~165 MB to ~15 MB.
4. **Alpine will not work.** turso-go ships a static archive for musl, which
   `purego` cannot `dlopen`. The runtime image is `distroless/cc` — `/base` is
   missing `libgcc_s.so.1`, which the Rust engine links against.

## Deployment

Push to `main` runs tests, builds a multi-arch image to GHCR, and deploys over
SSH. The repository is public, so CI runs on GitHub-hosted runners — a
self-hosted runner would let any fork's pull request execute code on the host.
See `.github/workflows/ci.yml` for the required secrets.

## Chemistry accuracy

The chemistry engine is calibrated against a real Pulse Hydrometrics report and
tested against it (`internal/chem/chem_test.go`). For a 58,000 L vinyl salt pool
reading pH 7.30 / TA 106 / CH 159 / CYA 5 / 21 °C, it reproduces:

| | Report | Computed |
|---|---|---|
| Adjusted alkalinity | 104.33 | 104.33 |
| Saturation index (WQI) | −0.49 | −0.48 |
| Salt to add (target 3200 ppm) | 60.262 kg | 60.26 kg |
| Stabilizer to add (target 40 ppm) | 2.03 kg | 2.03 kg |

Doses are computed for commodity chemical grades, which is why each one names the
product strength it assumes. A store's branded equivalent may differ.

This is a tool for tracking your own pool, not a substitute for a professional
water test. Handle pool chemicals per their label instructions and never mix them.
