package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The CDN rewrites the origin's no-cache into a four-hour max-age, so a change
// to the interface reaches nobody until it expires unless the URL itself
// changes. These pin the mechanism that prevents that.
func testServer(t *testing.T, files fstest.MapFS) *Server {
	t.Helper()
	return &Server{Static: files}
}

func TestAssetURLsCarryTheirFingerprint(t *testing.T) {
	files := fstest.MapFS{
		"app.html":     {Data: []byte(`<script src="/js/app.js?v=__BUILD__"></script>`)},
		"js/app.js":    {Data: []byte(`import x from './api.js?v=__BUILD__';`)},
		"js/api.js":    {Data: []byte(`export const api = 1;`)},
		"js/charts.js": {Data: []byte(`export const c = 1;`)},
		"css/app.css":  {Data: []byte(`body{}`)},
	}
	s := testServer(t, files)

	rec := httptest.NewRecorder()
	s.serveStatic(rec, httptest.NewRequest(http.MethodGet, "/app.html", nil))
	body := rec.Body.String()

	if strings.Contains(body, "__BUILD__") {
		t.Fatalf("the placeholder was served raw: %q", body)
	}
	stamp := s.assetStamp()
	if !strings.Contains(body, "/js/app.js?v="+stamp) {
		t.Errorf("body = %q, want the script tagged with %s", body, stamp)
	}
	// The shell names the fingerprinted assets, so it must never be cached.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on the shell", cc)
	}

	// A module's own imports need the same treatment, or the browser reuses a
	// stale sibling while the entry point is fresh.
	rec = httptest.NewRecorder()
	s.serveStatic(rec, httptest.NewRequest(http.MethodGet, "/js/app.js?v="+stamp, nil))
	if got := rec.Body.String(); !strings.Contains(got, "./api.js?v="+stamp) {
		t.Errorf("module body = %q, want its import fingerprinted", got)
	}
	// A fingerprinted URL is safe to cache forever.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want a fingerprinted asset cached hard", cc)
	}
}

// The fingerprint has to follow the content, or a redeploy that changes a
// script keeps serving the old URL.
func TestFingerprintFollowsTheContent(t *testing.T) {
	base := func(js string) fstest.MapFS {
		return fstest.MapFS{
			"js/app.js":    {Data: []byte(js)},
			"js/api.js":    {Data: []byte(`export const api = 1;`)},
			"js/charts.js": {Data: []byte(`export const c = 1;`)},
			"css/app.css":  {Data: []byte(`body{}`)},
		}
	}
	before := testServer(t, base(`const a = 1;`)).assetStamp()
	after := testServer(t, base(`const a = 2;`)).assetStamp()
	if before == after {
		t.Error("the fingerprint did not change when a script did")
	}
	// And it must be stable for identical content, so an unchanged redeploy
	// does not throw away everyone's cache.
	if again := testServer(t, base(`const a = 1;`)).assetStamp(); again != before {
		t.Errorf("fingerprint = %s then %s for identical assets", before, again)
	}
}

var _ fs.FS = fstest.MapFS{}
