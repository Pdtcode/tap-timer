package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"taptimer/internal/config"
	"taptimer/internal/i18n"
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

// get requests target on the canonical host (testConfig's BASE_URL).
func get(r http.Handler, target string) *httptest.ResponseRecorder {
	return do(r, http.MethodGet, "taptimer.test", target)
}

func do(r http.Handler, method, host, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	r.ServeHTTP(w, req)
	return w
}

// The structured data must be valid JSON on every page (a template slip
// would silently drop it from search results), with a breadcrumb on inner pages.
func TestStructuredData(t *testing.T) {
	r := newTestRouter(t, testConfig())
	ldRe := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)
	for _, p := range []string{"/", "/party", "/online", "/how-to-play", "/privacy", "/terms"} {
		body := get(r, p).Body.String()
		m := ldRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no JSON-LD", p)
		}
		var ld struct {
			Graph []map[string]any `json:"@graph"`
		}
		if err := json.Unmarshal([]byte(m[1]), &ld); err != nil {
			t.Fatalf("%s: JSON-LD doesn't parse: %v\n%s", p, err, m[1])
		}
		crumb := false
		for _, node := range ld.Graph {
			if node["@type"] == "BreadcrumbList" {
				crumb = true
			}
		}
		if crumb != (p != "/") {
			t.Errorf("%s: breadcrumb present = %v", p, crumb)
		}
		if !strings.Contains(body, `max-image-preview:large`) {
			t.Errorf("%s: no max-image-preview robots tag", p)
		}
	}
	// Room links stay out of search entirely.
	if body := get(r, "/r/ABCD").Body.String(); strings.Contains(body, "max-image-preview") {
		t.Error("room page has the indexing robots tag")
	}
}

func TestPagesRender(t *testing.T) {
	r := newTestRouter(t, testConfig())
	for _, p := range []string{"/", "/party", "/how-to-play", "/privacy", "/terms"} {
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
	// Without AdSense, house ads fill every slot (all three on the home page
	// have campaigns), leaving nothing for a placeholder and no AdSense script.
	for _, env := range []string{"development", "production"} {
		cfg := testConfig()
		cfg.Env = env
		body := get(newTestRouter(t, cfg), "/").Body.String()
		if strings.Contains(body, "ad--placeholder") || strings.Contains(body, "adsbygoogle") {
			t.Errorf("%s without AdSense: placeholder or AdSense markup rendered", env)
		}
		if n := strings.Count(body, "data-house>"); n != 3 {
			t.Errorf("%s without AdSense: %d house slots, want 3", env, n)
		}
	}

	// Configured: loader script plus real units for slots that have ids.
	cfg := testConfig()
	cfg.Env = "production"
	cfg.AdSenseClient = "ca-pub-1234567890"
	cfg.AdSlots = map[string]string{"incontent": "111", "sidebar": "222"}
	body := get(newTestRouter(t, cfg), "/").Body.String()
	for _, want := range []string{
		`adsbygoogle.js?client=ca-pub-1234567890`,
		`data-ad-slot="111"`,
		`data-ad-slot="222"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// House ads still ride along, as the fallback for unfilled AdSense.
	if n := strings.Count(body, "data-house>"); n != 3 {
		t.Errorf("with AdSense: %d house slots, want 3", n)
	}
}

func TestHouseAdSlots(t *testing.T) {
	// Every campaign targeting the slot is sent, with all its creatives, so
	// app.js can rotate over the slots actually shown and fall back to a size
	// that fits. Local creatives carry the asset version; remote ones don't.
	hs := resolveHouseSlot("sidebar", "abc")
	var got []string
	for _, c := range hs.Campaigns {
		for _, f := range c.Frames {
			got = append(got, c.ID+" "+f.Src)
		}
	}
	want := []string{
		"grail-seekers /static/gs-banner.html?size=300x600&theme=dark&v=abc",
		"grail-seekers /static/gs-banner.html?size=300x250&theme=dark&v=abc",
		"han-jan-pocha " + hanJanSrc("square"),
		"han-jan-pocha " + hanJanSrc("rect"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sidebar creatives:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if resolveHouseSlot("nonexistent", "abc") != nil {
		t.Error("slot with no campaigns should resolve to nil")
	}
}

func TestHouseAdCreativeServed(t *testing.T) {
	w := get(newTestRouter(t, testConfig()), "/static/gs-banner.html?size=300x250&theme=dark")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "utm_source=taptimer") {
		t.Errorf("gs-banner.html: status %d, retagged utm_source present = %v",
			w.Code, strings.Contains(w.Body.String(), "utm_source=taptimer"))
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
	if body := get(r, "/robots.txt").Body.String(); !strings.Contains(body, "Sitemap: https://taptimer.test/sitemap.xml") || !strings.Contains(body, "Disallow: /studio") {
		t.Errorf("robots: %s", body)
	}
}

func TestHeadRequests(t *testing.T) {
	r := newTestRouter(t, testConfig())
	for _, p := range []string{"/", "/party", "/how-to-play", "/privacy", "/terms", "/robots.txt", "/sitemap.xml"} {
		if w := do(r, http.MethodHead, "taptimer.test", p); w.Code != http.StatusOK {
			t.Errorf("HEAD %s: status %d", p, w.Code)
		}
	}
}

func TestCanonicalHostRedirect(t *testing.T) {
	cfg := testConfig()
	cfg.Env = "production"
	r := newTestRouter(t, cfg)

	w := do(r, http.MethodGet, "www.taptimer.test", "/party?x=1")
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "https://taptimer.test/party?x=1" {
		t.Errorf("other host: status %d, location %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(r, http.MethodGet, "10.0.0.5:8080", "/healthz"); w.Code != http.StatusOK {
		t.Errorf("healthz on internal host: status %d, want 200", w.Code)
	}
	if w := get(r, "/party"); w.Code != http.StatusOK {
		t.Errorf("canonical host: status %d", w.Code)
	}

	// Development serves any host (localhost, LAN IPs for phone testing).
	if w := do(newTestRouter(t, testConfig()), http.MethodGet, "192.168.1.20:8080", "/"); w.Code != http.StatusOK {
		t.Errorf("dev on another host: status %d, want 200", w.Code)
	}
}

func TestSEOHead(t *testing.T) {
	cfg := testConfig()
	cfg.GoogleSiteVerification = "g-token"
	cfg.BingSiteVerification = "b-token"
	r := newTestRouter(t, cfg)
	body := get(r, "/").Body.String()
	for _, want := range []string{
		`<meta name="google-site-verification" content="g-token">`,
		`<meta name="msvalidate.01" content="b-token">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	m := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("JSON-LD block not found")
	}
	var ld struct {
		Graph []struct {
			Type json.RawMessage `json:"@type"` // a string, or a list for a node with several types
			URL  string          `json:"url"`
		} `json:"@graph"`
	}
	if err := json.Unmarshal([]byte(m[1]), &ld); err != nil {
		t.Fatalf("JSON-LD is not valid JSON: %v\n%s", err, m[1])
	}
	var types []string
	for _, n := range ld.Graph {
		var one string
		var many []string
		if json.Unmarshal(n.Type, &one) != nil {
			json.Unmarshal(n.Type, &many)
			one = strings.Join(many, "+")
		}
		types = append(types, one)
		if n.URL != "https://taptimer.test/" {
			t.Errorf("%s url = %q", one, n.URL)
		}
	}
	if strings.Join(types, ",") != "WebSite,VideoGame+WebApplication,MobileApplication" {
		t.Errorf("JSON-LD types = %v", types)
	}

	if body := get(r, "/nope").Body.String(); !strings.Contains(body, `<meta name="robots" content="noindex">`) || strings.Contains(body, `rel="canonical"`) {
		t.Error("404 should be noindex with no canonical")
	}
}

// A studio that can't start must leave the rest of the site up.
func TestStudioFailureKeepsSiteUp(t *testing.T) {
	cfg := testConfig()
	cfg.Studio = config.Studio{
		Password: "correct horse battery",
		Secret:   strings.Repeat("s", 32),
		TZ:       "Mars/Base", // config.Load would refuse this; here it makes studio.New fail
	}
	r := newTestRouter(t, cfg)
	if w := get(r, "/"); w.Code != http.StatusOK {
		t.Errorf("home: status %d", w.Code)
	}
	if w := get(r, "/studio/login"); w.Code != http.StatusNotFound {
		t.Errorf("studio: status %d, want 404 when disabled", w.Code)
	}
}

// Every page exists in every language, says which language it is, links
// within that language, and lists all the versions for search engines.
func TestLocalizedPages(t *testing.T) {
	r := newTestRouter(t, testConfig())
	for _, l := range i18n.Langs {
		for _, p := range sitePages {
			url := l.Path(p.path)
			w := get(r, url)
			if w.Code != http.StatusOK {
				t.Errorf("%s: status %d", url, w.Code)
				continue
			}
			body := w.Body.String()
			for _, want := range []string{
				`<html lang="` + l.Code + `"`,
				`<link rel="canonical" href="https://taptimer.test` + url + `">`,
				`href="` + l.Path("/party") + `"`,
				`hreflang="x-default" href="https://taptimer.test` + p.path + `"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s: missing %q", url, want)
				}
			}
			for _, al := range i18n.Langs {
				if !strings.Contains(body, `hreflang="`+al.Hreflang+`" href="https://taptimer.test`+al.Path(p.path)+`"`) {
					t.Errorf("%s: no hreflang link to %s", url, al.Code)
				}
			}
		}
		// Room links and 404s in each language too.
		if w := get(r, l.Path("/r/K7QX")); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<html lang="`+l.Code+`"`) {
			t.Errorf("%s: status %d", l.Path("/r/K7QX"), w.Code)
		}
		if w := get(r, l.Path("/nope")); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `<html lang="`+l.Code+`"`) {
			t.Errorf("%s: status %d", l.Path("/nope"), w.Code)
		}
	}
	// /es redirects to /es/ (one URL per page).
	if w := get(r, "/es"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/es/" {
		t.Errorf("/es: %d %q", w.Code, w.Header().Get("Location"))
	}
	// The sitemap lists every language version.
	sm := get(r, "/sitemap.xml").Body.String()
	for _, want := range []string{"<loc>https://taptimer.test/de/party</loc>", `hreflang="pt-BR" href="https://taptimer.test/pt/how-to-play"`} {
		if !strings.Contains(sm, want) {
			t.Errorf("sitemap missing %q", want)
		}
	}
}
