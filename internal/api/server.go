// Package api serves the JSON API and the web application.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	goapi "github.com/anchoo2kewl/go-api"
	gophoto "github.com/anchoo2kewl/go-photo"

	"github.com/biswas-dev/pool/internal/ai"
	"github.com/biswas-dev/pool/internal/api/spec"
	"github.com/biswas-dev/pool/internal/auth"
	"github.com/biswas-dev/pool/internal/config"
	"github.com/biswas-dev/pool/internal/store"
)

// Server holds the dependencies shared by every handler.
type Server struct {
	DB     *store.DB
	Cfg    *config.Config
	Static fs.FS
	// AI is the server-wide provider chain. A user with their own key gets a
	// chain of their own instead; see aiService.
	AI *ai.Service

	// The attachment store is built on first upload, so a server that never
	// receives one never creates the directory.
	photoOnce sync.Once
	photos    *gophoto.Store
	photoErr  error
}

// New builds the server. A nil AI service simply leaves the AI features off.
func New(db *store.DB, cfg *config.Config, static fs.FS, aiSvc *ai.Service) *Server {
	if aiSvc == nil {
		aiSvc = ai.New(nil, nil)
	}
	return &Server{DB: db, Cfg: cfg, Static: static, AI: aiSvc}
}

const sessionCookie = "pool_session"

// Routes returns the fully-wired HTTP handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// ── Public ────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Ping(); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /api/config", s.handleClientConfig)
	// The OpenAPI document is public by design: requiring a credential to
	// discover how to present a credential is a loop.
	mux.Handle("GET "+goapi.SpecPath, spec.Document.Handler())
	mux.Handle("HEAD "+goapi.SpecPath, spec.Document.Handler())

	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/auth/demo", s.handleDemoLogin)
	mux.HandleFunc("GET /auth/{provider}/start", s.handleOAuthStart)
	mux.HandleFunc("GET /auth/{provider}/callback", s.handleOAuthCallback)

	// ── Authenticated: session cookie or API key ──────────────────────────
	authed := func(h http.HandlerFunc) http.Handler { return s.requireAuth(h) }

	mux.Handle("GET /api/me", authed(s.handleMe))
	mux.Handle("PATCH /api/me", authed(s.handleUpdateMe))
	mux.Handle("PUT /api/me/ai", authed(s.handleSetAISettings))
	mux.Handle("POST /api/demo/reset", authed(s.handleDemoReset))

	mux.Handle("GET /api/keys", authed(s.handleListAPIKeys))
	mux.Handle("POST /api/keys", authed(s.handleCreateAPIKey))
	mux.Handle("DELETE /api/keys/{id}", authed(s.handleRevokeAPIKey))

	mux.Handle("GET /api/pools", authed(s.handleListPools))
	mux.Handle("POST /api/pools", authed(s.handleCreatePool))
	mux.Handle("GET /api/pools/{id}", authed(s.handleGetPool))
	mux.Handle("PATCH /api/pools/{id}", authed(s.handleUpdatePool))
	mux.Handle("DELETE /api/pools/{id}", authed(s.handleDeletePool))

	mux.Handle("GET /api/companies", authed(s.handleListCompanies))
	mux.Handle("POST /api/companies", authed(s.handleCreateCompany))
	mux.Handle("PATCH /api/companies/{id}", authed(s.handleUpdateCompany))
	mux.Handle("DELETE /api/companies/{id}", authed(s.handleDeleteCompany))

	mux.Handle("GET /api/tests", authed(s.handleListTests))
	mux.Handle("POST /api/tests", authed(s.handleCreateTest))
	mux.Handle("POST /api/tests/from-photo", authed(s.handleCreateTestFromPhoto))
	mux.Handle("GET /api/tests/{id}", authed(s.handleGetTest))
	mux.Handle("PATCH /api/tests/{id}", authed(s.handleUpdateTest))
	mux.Handle("DELETE /api/tests/{id}", authed(s.handleDeleteTest))
	mux.Handle("POST /api/tests/{id}/insight", authed(s.handleGenerateInsight))
	mux.Handle("POST /api/treatments/{id}/applied", authed(s.handleMarkTreatmentApplied))

	mux.Handle("GET /api/notes", authed(s.handleListNotes))
	mux.Handle("POST /api/notes", authed(s.handleCreateNote))
	mux.Handle("PATCH /api/notes/{id}", authed(s.handleUpdateNote))
	mux.Handle("DELETE /api/notes/{id}", authed(s.handleDeleteNote))

	mux.Handle("GET /api/seasons", authed(s.handleListSeasons))
	mux.Handle("POST /api/seasons", authed(s.handleCreateSeason))
	mux.Handle("PATCH /api/seasons/{id}", authed(s.handleUpdateSeason))
	mux.Handle("DELETE /api/seasons/{id}", authed(s.handleDeleteSeason))

	mux.Handle("GET /api/log", authed(s.handleListLog))
	mux.Handle("POST /api/log", authed(s.handleCreateLog))
	mux.Handle("PATCH /api/log/{id}", authed(s.handleUpdateLog))
	mux.Handle("DELETE /api/log/{id}", authed(s.handleDeleteLog))

	mux.Handle("GET /api/attachments", authed(s.handleListAttachments))
	mux.Handle("POST /api/attachments", authed(s.handleUploadAttachment))
	mux.Handle("PATCH /api/attachments/{id}", authed(s.handleUpdateAttachment))
	mux.Handle("DELETE /api/attachments/{id}", authed(s.handleDeleteAttachment))
	mux.Handle("GET /api/attachments/{id}/file", authed(s.handleServeAttachment))
	mux.Handle("POST /api/attachments/{id}/link", authed(s.handleLinkAttachment))
	mux.Handle("POST /api/attachments/{id}/parse", authed(s.handleParseAttachment))

	mux.Handle("GET /api/analytics/summary", authed(s.handleAnalyticsSummary))
	mux.Handle("GET /api/analytics/costs", authed(s.handleAnalyticsCosts))
	mux.Handle("GET /api/analytics/trends", authed(s.handleAnalyticsTrends))

	// ── Static application ────────────────────────────────────────────────
	mux.HandleFunc("/", s.serveStatic)

	return s.withCommon(mux)
}

// withCommon applies logging, security headers, and panic recovery.
func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic %s %s: %v", r.Method, r.URL.Path, rec)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		if !strings.HasPrefix(r.URL.Path, "/assets/") && r.URL.Path != "/healthz" {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// ── Authentication middleware ────────────────────────────────────────────

type ctxKey string

const userKey ctxKey = "user"

// requireAuth accepts either a session cookie (browser) or a bearer API key
// (scripts and agents), so every endpoint works both ways.
func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.resolveUser(r)
		if errors.Is(err, errNoCredential) && bearerKey(r) == "" && !hasSession(r) {
			// Nothing was presented at all, which is worth saying plainly:
			// go-api's message is about a credential that was offered and
			// refused, and reads as a lie when none was.
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			// go-api distinguishes the one case where presenting a different
			// credential of the same kind would help — a read key on a write
			// endpoint is a 403, not a 401 — and collapses every other
			// failure to one message, so a prober cannot learn which key
			// strings were once real.
			writeError(w, goapi.StatusFor(err), goapi.PublicMessage(err))
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r, user)))
	})
}

// errNoCredential is the failure when nothing was presented at all. go-api
// maps it to a 401 alongside every other authentication failure.
var errNoCredential = goapi.ErrNotFound

// currentUser resolves the caller from a session cookie or an API key.
//
// Two key formats are accepted. A go-api token, recognised by its prefix and
// shape, is verified by go-api — which enforces revocation, expiry and the
// read/write scope against the request method. A key issued before that
// existed falls through to the original lookup, so nobody's script stops
// working; those carry no scope enforcement beyond revocation and expiry,
// which is what they were issued under.
func (s *Server) resolveUser(r *http.Request) (*store.User, error) {
	if key := bearerKey(r); key != "" {
		if TokenScheme.Issued(key) {
			return s.authenticateToken(r, key)
		}
		u, _, err := s.DB.UserByAPIKeyHash(auth.HashAPIKey(key))
		if err != nil {
			return nil, errNoCredential
		}
		return u, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errNoCredential
	}
	u, err := s.DB.UserBySession(c.Value)
	if err != nil {
		return nil, errNoCredential
	}
	return u, nil
}

// currentUser is resolveUser for the places that only need to know whether
// somebody is signed in — serving the app shell, and the redirects around it.
func (s *Server) currentUser(r *http.Request) *store.User {
	u, err := s.resolveUser(r)
	if err != nil {
		return nil
	}
	return u
}

// hasSession reports whether a session cookie was sent, regardless of whether
// it is still valid.
func hasSession(r *http.Request) bool {
	_, err := r.Cookie(sessionCookie)
	return err == nil
}

// bearerKey pulls an API key out of either header a caller might use.
//
// A plain X-API-Key header is accepted alongside the bearer token because it
// is what most scripts reach for first.
func bearerKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// tokenAuthed reports whether the request presented an API key rather than a
// session. It is what stops a key from minting another key.
func tokenAuthed(r *http.Request) bool { return bearerKey(r) != "" }

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// ── Static files ─────────────────────────────────────────────────────────

// serveStatic serves the embedded web application. Unknown paths fall back to
// the app shell so client-side routes survive a refresh.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "no such endpoint")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		// The landing page is public; signed-in visitors go straight to the app.
		if s.currentUser(r) != nil {
			http.Redirect(w, r, "/app", http.StatusFound)
			return
		}
		path = "index.html"
	}

	switch path {
	case "app", "app/":
		if s.currentUser(r) == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		path = "app.html"
	case "login", "login/":
		if s.currentUser(r) != nil {
			http.Redirect(w, r, "/app", http.StatusFound)
			return
		}
		path = "login.html"
	case "docs", "docs/", "api", "api/":
		path = "docs.html"
	}

	f, err := s.Static.Open(path)
	if err != nil {
		// Unknown path: fall back to the landing page rather than a bare 404.
		if f, err = s.Static.Open("index.html"); err != nil {
			http.NotFound(w, r)
			return
		}
		path = "index.html"
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(path, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	rs, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
}

// ── JSON helpers ─────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps a store error onto an HTTP status.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		log.Printf("store error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid request body: " + err.Error())
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func queryInt(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}
