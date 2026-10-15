package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"taptimer/internal/config"
	"taptimer/internal/studio/platform"
)

const testBase = "https://taptimer.test"

func testConfig() config.Config {
	return config.Config{
		Env:     "development",
		BaseURL: testBase,
		Studio: config.Studio{
			Password: "correct horse battery",
			Secret:   strings.Repeat("s", 32),
			TZ:       "America/Chicago",
			Tokens:   map[string]string{},
		},
	}
}

func newTestStudio(t *testing.T, cfg config.Config) (*Studio, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s, err := New(cfg, template.FuncMap{"asset": func(p string) string { return "/static/" + p }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.secretsDir = t.TempDir()
	r := gin.New()
	s.Register(r)
	return s, r
}

// fake is a platform that records uploads and serves a canned history.
type fake struct {
	name     string
	got      []byte
	caption  string
	failWith error
	posts    []platform.Post
}

func (f *fake) Name() string     { return f.name }
func (f *fake) Label() string    { return "Fake " + f.name }
func (f *fake) Src() string      { return f.name }
func (f *fake) Configured() bool { return true }
func (f *fake) AuthURL(state, redirect string) string {
	return "https://auth.test/?state=" + url.QueryEscape(state)
}
func (f *fake) Exchange(ctx context.Context, code, redirect string) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "a", RefreshToken: "refresh-" + code}, nil
}
func (f *fake) TokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	return oauth2.StaticTokenSource(tok)
}
func (f *fake) TokenEnv() string                        { return "FAKE_TOKEN" }
func (f *fake) SavedToken(tok *oauth2.Token) string     { return tok.RefreshToken }
func (f *fake) RestoreToken(saved string) *oauth2.Token { return &oauth2.Token{RefreshToken: saved} }
func (f *fake) Account(ctx context.Context, ts oauth2.TokenSource) (platform.Account, error) {
	return platform.Account{Handle: "@" + f.name}, nil
}
func (f *fake) Upload(ctx context.Context, ts oauth2.TokenSource, v platform.Video) (platform.Result, error) {
	if f.failWith != nil {
		return platform.Result{}, f.failWith
	}
	f.got, _ = io.ReadAll(v.Body)
	f.caption = v.Caption
	return platform.Result{RemoteID: "v1", URL: "https://" + f.name + ".test/v1", Status: platform.StatusPublished}, nil
}
func (f *fake) History(ctx context.Context, ts oauth2.TokenSource, since time.Time) ([]platform.Post, error) {
	return f.posts, nil
}

// useFakes swaps the real platforms for fakes; connected ones get a token.
func useFakes(s *Studio, fakes []*fake, connected ...string) {
	s.platforms = nil
	s.conns = map[string]*conn{}
	for _, f := range fakes {
		s.platforms = append(s.platforms, f)
		c := &conn{}
		for _, name := range connected {
			if name == f.name {
				c.ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"})
			}
		}
		s.conns[f.name] = c
	}
}

func request(r http.Handler, method, target string, body io.Reader, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodPost && body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func signIn(t *testing.T, r http.Handler) *http.Cookie {
	t.Helper()
	w := request(r, http.MethodPost, "/studio/login", strings.NewReader("password=correct+horse+battery"), nil, testBase)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login: status %d", w.Code)
	}
	return cookieNamed(t, w, sessionCookie)
}

func cookieNamed(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s cookie", name)
	return nil
}

var pages = []string{"/studio", "/studio/record", "/studio/new", "/studio/connections"}

func TestPagesNeedSignIn(t *testing.T) {
	_, r := newTestStudio(t, testConfig())
	for _, p := range pages {
		if w := request(r, http.MethodGet, p, nil, nil, ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/studio/login" {
			t.Errorf("%s signed out: status %d, Location %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	if w := request(r, http.MethodPost, "/studio/api/upload", strings.NewReader("x"), nil, testBase); w.Code != http.StatusUnauthorized {
		t.Errorf("upload signed out: status %d, want 401", w.Code)
	}
	if w := request(r, http.MethodGet, "/studio/login", nil, nil, ""); w.Code != http.StatusOK || w.Header().Get("X-Robots-Tag") == "" {
		t.Errorf("login page: status %d, X-Robots-Tag %q", w.Code, w.Header().Get("X-Robots-Tag"))
	}

	cookie := signIn(t, r)
	for _, p := range pages {
		if w := request(r, http.MethodGet, p, nil, cookie, ""); w.Code != http.StatusOK {
			t.Errorf("%s signed in: status %d: %s", p, w.Code, w.Body.String())
		}
	}
}

func TestManifest(t *testing.T) {
	_, r := newTestStudio(t, testConfig())
	w := request(r, http.MethodGet, "/studio/manifest.webmanifest", nil, nil, "") // signed out
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("status %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}
	var m struct{ Scope, StartURL, Display string }
	if err := json.Unmarshal(w.Body.Bytes(), &struct {
		Scope    *string `json:"scope"`
		StartURL *string `json:"start_url"`
		Display  *string `json:"display"`
	}{&m.Scope, &m.StartURL, &m.Display}); err != nil {
		t.Fatal(err)
	}
	if m.Scope != "/studio" || m.StartURL != "/studio" || m.Display != "standalone" {
		t.Errorf("manifest = %+v", m)
	}
	// Every page links it and carries the tab index the highlight slides to.
	page := request(r, http.MethodGet, "/studio/record", nil, signIn(t, r), "").Body.String()
	for _, want := range []string{`rel="manifest" href="/studio/manifest.webmanifest"`, `data-tab="1"`, "st-tabs__pill"} {
		if !strings.Contains(page, want) {
			t.Errorf("record page missing %q", want)
		}
	}
}

func TestLogin(t *testing.T) {
	_, r := newTestStudio(t, testConfig())
	if w := request(r, http.MethodPost, "/studio/login", strings.NewReader("password=nope"), nil, testBase); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d", w.Code)
	}
	// Cross-site posts are refused before the password is even checked.
	if w := request(r, http.MethodPost, "/studio/login", strings.NewReader("password=correct+horse+battery"), nil, "https://evil.test"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site login: status %d, want 403", w.Code)
	}
	for range loginFailLimit {
		request(r, http.MethodPost, "/studio/login", strings.NewReader("password=nope"), nil, testBase)
	}
	if w := request(r, http.MethodPost, "/studio/login", strings.NewReader("password=correct+horse+battery"), nil, testBase); w.Code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: status %d, want 429", loginFailLimit, w.Code)
	}
}

func TestSessionCookie(t *testing.T) {
	s, _ := newTestStudio(t, testConfig())
	now := time.Now()
	v := s.newSession(now)
	if !s.validSession(v, now) {
		t.Fatal("fresh session rejected")
	}
	if s.validSession(v, now.Add(sessionTTL+time.Minute)) {
		t.Error("expired session accepted")
	}
	raw := []byte(v)
	raw[3] ^= 1
	if s.validSession(string(raw), now) {
		t.Error("tampered session accepted")
	}
}

func TestSavedLoginRestored(t *testing.T) {
	cfg := testConfig()
	cfg.Studio.YouTubeClientID, cfg.Studio.YouTubeClientSecret = "id", "secret"
	cfg.Studio.Tokens["YOUTUBE_REFRESH_TOKEN"] = "saved-refresh"
	cfg.Studio.Tokens["TIKTOK_REFRESH_TOKEN"] = "ignored: TikTok's app isn't configured"
	s, _ := newTestStudio(t, cfg)
	if c := s.conns["youtube"]; c.ts == nil || !c.saved {
		t.Errorf("youtube conn = %+v, want connected and saved", c)
	}
	if c := s.conns["tiktok"]; c.ts != nil {
		t.Error("tiktok connected without a developer app")
	}
}

func meta(t *testing.T, m uploadMeta) string {
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func upload(r http.Handler, cookie *http.Cookie, header, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/studio/api/upload", strings.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("Origin", testBase)
	req.Header.Set("X-Studio-Post", header)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestUpload(t *testing.T) {
	s, r := newTestStudio(t, testConfig())
	yt, ig, tt := &fake{name: "yt"}, &fake{name: "ig", failWith: errors.New("meta is down")}, &fake{name: "tt"}
	useFakes(s, []*fake{yt, ig, tt}, "yt", "ig")
	cookie := signIn(t, r)

	h := meta(t, uploadMeta{Title: "Hit 10.00", Captions: map[string]string{"yt": " cap yt ", "ig": "cap ig", "tt": "cap tt", "nope": "x"}})
	w := upload(r, cookie, h, "fake video bytes")
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct{ Results []uploadResult }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v", resp.Results)
	}
	if r := resp.Results[0]; r.Platform != "yt" || r.Status != platform.StatusPublished || r.URL != "https://yt.test/v1" {
		t.Errorf("yt = %+v", r)
	}
	if r := resp.Results[1]; r.Status != platform.StatusFailed || r.Error != "meta is down" {
		t.Errorf("ig = %+v", r)
	}
	if r := resp.Results[2]; r.Status != platform.StatusFailed || !strings.Contains(r.Error, "isn't connected") {
		t.Errorf("tt = %+v", r)
	}
	if string(yt.got) != "fake video bytes" || yt.caption != "cap yt" {
		t.Errorf("yt got %q, caption %q", yt.got, yt.caption)
	}

	if w := upload(r, cookie, "not base64 json", "x"); w.Code != http.StatusBadRequest {
		t.Errorf("bad header: status %d", w.Code)
	}
}

func TestParseUploadMeta(t *testing.T) {
	enc := func(m uploadMeta) string { return meta(t, m) }
	if m, err := parseUploadMeta(enc(uploadMeta{Title: " Hit 10.00 ", Captions: map[string]string{"youtube": "a"}})); err != nil || m.Title != "Hit 10.00" {
		t.Errorf("ok: %+v, %v", m, err)
	}
	for name, h := range map[string]string{
		"no title":     enc(uploadMeta{Captions: map[string]string{"youtube": "a"}}),
		"long title":   enc(uploadMeta{Title: strings.Repeat("x", 101), Captions: map[string]string{"youtube": "a"}}),
		"no platforms": enc(uploadMeta{Title: "x"}),
		"garbage":      "%%%",
	} {
		if _, err := parseUploadMeta(h); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Emoji survive the base64 header (the page encodes UTF-8 bytes).
	if m, err := parseUploadMeta(enc(uploadMeta{Title: "Perfect 5.00 🔥", Captions: map[string]string{"youtube": "a"}})); err != nil || m.Title != "Perfect 5.00 🔥" {
		t.Errorf("emoji: %+v, %v", m, err)
	}
}

func TestDashboardReadsHistory(t *testing.T) {
	s, r := newTestStudio(t, testConfig())
	now := time.Now()
	yt := &fake{name: "yt", posts: []platform.Post{
		{ID: "a", Title: "Newest clip", URL: "https://yt.test/a", PostedAt: now, Privacy: "private", Stats: platform.Stats{Views: 42}},
		{ID: "b", Title: "Yesterday clip", URL: "https://yt.test/b", PostedAt: now.AddDate(0, 0, -1), Privacy: "public"},
	}}
	useFakes(s, []*fake{yt}, "yt")
	w := request(r, http.MethodGet, "/studio", nil, signIn(t, r), "")
	body := w.Body.String()
	for _, want := range []string{"Newest clip", "Yesterday clip", "42 views", "private", `<div class="st-streak__num">2</div>`, "@yt"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestConnectSavesLocally(t *testing.T) {
	s, r := newTestStudio(t, testConfig())
	f := &fake{name: "yt"}
	useFakes(s, []*fake{f})
	cookie := signIn(t, r)

	w := request(r, http.MethodGet, "/studio/connect/yt", nil, cookie, "")
	loc, _ := url.Parse(w.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatalf("connect: Location %q", w.Header().Get("Location"))
	}
	stateC := cookieNamed(t, w, stateCookie)

	req := httptest.NewRequest(http.MethodGet, "/studio/connect/yt/callback?code=xyz&state="+url.QueryEscape(state), nil)
	req.AddCookie(cookie)
	req.AddCookie(stateC)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Saved to") {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(s.secretsDir, "FAKE_TOKEN"))
	if err != nil || strings.TrimSpace(string(b)) != "refresh-xyz" {
		t.Errorf("saved file = %q, %v", b, err)
	}
	if c := s.conns["yt"]; c.ts == nil || !c.saved {
		t.Errorf("conn = %+v", c)
	}
}

func TestConnectShowsSecretInProduction(t *testing.T) {
	cfg := testConfig()
	cfg.Env = "production"
	s, r := newTestStudio(t, cfg)
	useFakes(s, []*fake{{name: "yt"}})
	cookie := signIn(t, r)
	w := request(r, http.MethodGet, "/studio/connect/yt", nil, cookie, "")
	loc, _ := url.Parse(w.Header().Get("Location"))
	req := httptest.NewRequest(http.MethodGet, "/studio/connect/yt/callback?code=xyz&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	req.AddCookie(cookie)
	req.AddCookie(cookieNamed(t, w, stateCookie))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "fly secrets set FAKE_TOKEN=refresh-xyz") {
		t.Errorf("production callback doesn't show the command: %s", w.Body.String())
	}
	if entries, _ := os.ReadDir(s.secretsDir); len(entries) != 0 {
		t.Error("production wrote a secrets file")
	}
	if s.conns["yt"].saved {
		t.Error("marked saved before the secret is set")
	}
}

func TestOAuthStateChecked(t *testing.T) {
	_, r := newTestStudio(t, testConfig())
	cookie := signIn(t, r)
	q := url.Values{"state": {"forged"}, "code": {"x"}}
	if w := request(r, http.MethodGet, "/studio/connect/youtube/callback?"+q.Encode(), nil, cookie, ""); w.Code != http.StatusBadRequest {
		t.Errorf("callback without state cookie: status %d, want 400", w.Code)
	}
	// Not configured: back to Accounts with a message, no redirect to Google.
	if w := request(r, http.MethodGet, "/studio/connect/youtube", nil, cookie, ""); !strings.Contains(w.Header().Get("Location"), "unconfigured") {
		t.Errorf("connect unconfigured: Location %q", w.Header().Get("Location"))
	}
}

func TestStreak(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, loc) }
	now := day(14, 9)
	for _, tc := range []struct {
		name   string
		posted []time.Time
		want   int
	}{
		{"none", nil, 0},
		{"today only", []time.Time{day(14, 8)}, 1},
		{"not yet today", []time.Time{day(12, 20), day(13, 20)}, 2},
		{"gap", []time.Time{day(10, 20), day(12, 20), day(13, 20), day(14, 8)}, 3},
		{"two today", []time.Time{day(14, 1), day(14, 8)}, 1},
		{"broken", []time.Time{day(11, 20), day(12, 20)}, 0},
		// 11pm Chicago is the next day in UTC; it still counts as the 13th.
		{"late night", []time.Time{day(13, 23)}, 1},
	} {
		if got := streak(tc.posted, now, loc); got != tc.want {
			t.Errorf("%s: streak = %d, want %d", tc.name, got, tc.want)
		}
	}
	cal := calendar([]time.Time{day(14, 8)}, now, loc, 28)
	if len(cal) != 28 || !cal[27].Today || !cal[27].Posted || cal[26].Posted {
		t.Errorf("calendar tail = %+v", cal[26:])
	}
}

func TestFanout(t *testing.T) {
	data := bytes.Repeat([]byte("tap timer "), 100_000) // ~1 MB, several buffers
	var got1, got2 bytes.Buffer
	boom := errors.New("platform down")
	errs := fanout(bytes.NewReader(data), []func(io.Reader) error{
		func(r io.Reader) error { _, err := io.Copy(&got1, r); return err },
		func(r io.Reader) error { // fails part way through
			io.CopyN(io.Discard, r, 1000)
			return boom
		},
		func(r io.Reader) error { _, err := io.Copy(&got2, r); return err },
	})
	if errs[0] != nil || errs[2] != nil || !errors.Is(errs[1], boom) {
		t.Errorf("errs = %v", errs)
	}
	if !bytes.Equal(got1.Bytes(), data) || !bytes.Equal(got2.Bytes(), data) {
		t.Errorf("healthy sinks got %d and %d bytes, want %d", got1.Len(), got2.Len(), len(data))
	}
}
