package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"taptimer/internal/config"
)

func testConfig() config.Config {
	return config.Config{
		Env:        "development",
		Port:       "0",
		BaseURL:    "https://taptimer.test",
		AppStoreID: "6802904982",
		AdSlots:    map[string]string{},
	}
}

func newTestRouter(t *testing.T, cfg config.Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func get(r http.Handler, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestPagesRender(t *testing.T) {
	r := newTestRouter(t, testConfig())
	for _, p := range []string{"/", "/party", "/how-to-play", "/privacy"} {
		w := get(r, p)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", p, w.Code)
		}
		body := w.Body.String()
		for _, want := range []string{
			"<title>",
			`<link rel="canonical" href="https://taptimer.test` + p + `">`,
			`<meta name="apple-itunes-app" content="app-id=6802904982">`,
			"/get?src=header",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: body missing %q", p, want)
			}
		}
	}
}

func TestNotFound(t *testing.T) {
	w := get(newTestRouter(t, testConfig()), "/nope")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Page not found") {
		t.Fatalf("got %d", w.Code)
	}
}

func TestGetAppRedirect(t *testing.T) {
	cfg := testConfig()
	cfg.AppStoreProviderToken = "123456"
	r := newTestRouter(t, cfg)

	cases := map[string]string{
		"/get?src=home":                       "web-home",
		"/get?src=solo_nudge":                 "web-solo_nudge",
		"/get":                                "web-unknown",
		"/get?src=%3Cscript%3E":               "web-unknown",
		"/get?src=" + strings.Repeat("a", 40): "web-unknown",
	}
	for target, wantCT := range cases {
		w := get(r, target)
		if w.Code != http.StatusFound {
			t.Fatalf("%s: status %d", target, w.Code)
		}
		loc, err := url.Parse(w.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if loc.Host != "apps.apple.com" || loc.Path != "/app/apple-store/id6802904982" {
			t.Errorf("%s: redirected to %s", target, loc)
		}
		if got := loc.Query().Get("ct"); got != wantCT {
			t.Errorf("%s: ct=%q, want %q", target, got, wantCT)
		}
		if got := loc.Query().Get("pt"); got != "123456" {
			t.Errorf("%s: pt=%q", target, got)
		}
	}
}

func TestGetAppWithoutProviderToken(t *testing.T) {
	w := get(newTestRouter(t, testConfig()), "/get?src=home")
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Query().Has("pt") || loc.Query().Has("ct") {
		t.Errorf("unexpected campaign params without provider token: %s", loc)
	}
}

func TestAdsTxt(t *testing.T) {
	if w := get(newTestRouter(t, testConfig()), "/ads.txt"); w.Code != http.StatusNotFound {
		t.Errorf("ads.txt without client: status %d", w.Code)
	}

	cfg := testConfig()
	cfg.AdSenseClient = "ca-pub-1234567890"
	w := get(newTestRouter(t, cfg), "/ads.txt")
	if want := "google.com, pub-1234567890, DIRECT, f08c47fec0942fa0\n"; w.Body.String() != want {
		t.Errorf("ads.txt = %q, want %q", w.Body.String(), want)
	}
}

func TestAdUnits(t *testing.T) {
	// Dev without AdSense: placeholders, no AdSense script.
	body := get(newTestRouter(t, testConfig()), "/").Body.String()
	if !strings.Contains(body, "ad--placeholder") || strings.Contains(body, "adsbygoogle.js") {
		t.Error("dev mode should show placeholders and not load AdSense")
	}

	// Production without AdSense: nothing at all.
	cfg := testConfig()
	cfg.Env = "production"
	body = get(newTestRouter(t, cfg), "/").Body.String()
	if strings.Contains(body, "ad--placeholder") || strings.Contains(body, "adsbygoogle") {
		t.Error("production without AdSense should render no ad markup")
	}

	// Configured: loader script plus real units for slots that have ids.
	cfg.AdSenseClient = "ca-pub-1234567890"
	cfg.AdSlots = map[string]string{"incontent": "111", "sidebar": "222"}
	body = get(newTestRouter(t, cfg), "/").Body.String()
	for _, want := range []string{
		`adsbygoogle.js?client=ca-pub-1234567890`,
		`data-ad-slot="111"`,
		`data-ad-slot="222"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestStaticAssets(t *testing.T) {
	r := newTestRouter(t, testConfig())
	body := get(r, "/").Body.String()
	m := regexp.MustCompile(`/static/css/app\.css\?v=[0-9a-f]{10}`).FindString(body)
	if m == "" {
		t.Fatal("versioned stylesheet link not found")
	}
	w := get(r, m)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: status %d, cache-control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, dir := range []string{"/static/", "/static/css/"} {
		if w := get(r, dir); w.Code != http.StatusNotFound {
			t.Errorf("%s: directory listing status %d, want 404", dir, w.Code)
		}
	}
}

func TestOGImage(t *testing.T) {
	r := newTestRouter(t, testConfig())
	body := get(r, "/party").Body.String()
	m := regexp.MustCompile(`<meta property="og:image" content="https://taptimer\.test(/static/img/og\.png\?v=[0-9a-f]{10})">`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("absolute og:image meta tag not found")
	}
	if !strings.Contains(body, `<meta name="twitter:card" content="summary_large_image">`) {
		t.Error("twitter:card should be summary_large_image")
	}
	w := get(r, m[1])
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("og image: status %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestRootIcons(t *testing.T) {
	r := newTestRouter(t, testConfig())
	for _, p := range []string{"/favicon.ico", "/apple-touch-icon.png"} {
		w := get(r, p)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Body.Len() == 0 {
			t.Errorf("%s: status %d, type %q, %d bytes", p, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
}

func TestSitemapAndRobots(t *testing.T) {
	r := newTestRouter(t, testConfig())
	if body := get(r, "/sitemap.xml").Body.String(); !strings.Contains(body, "<loc>https://taptimer.test/party</loc>") {
		t.Errorf("sitemap: %s", body)
	}
	if body := get(r, "/robots.txt").Body.String(); !strings.Contains(body, "Sitemap: https://taptimer.test/sitemap.xml") {
		t.Errorf("robots: %s", body)
	}
}
