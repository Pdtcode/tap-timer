// Package server wires up the Gin router, templates and handlers.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"

	"github.com/gin-gonic/gin"

	"taptimer/internal/config"
	"taptimer/web"
)

type Server struct {
	cfg          config.Config
	pages        map[string]*template.Template
	assetVersion string
	online       *online
}

// App is the HTTP handler plus what main needs at shutdown.
type App struct {
	Handler *gin.Engine
	// Close tells players in Online rooms that the server is restarting.
	// Register it with http.Server.RegisterOnShutdown: Shutdown doesn't wait
	// for WebSockets.
	Close func()

	online *online // for tests
}

// AdUnit is the data passed to the "ad" partial.
type AdUnit struct {
	Name        string
	Client      string
	Slot        string
	Placeholder bool // dev-only dashed box so layouts can be checked without AdSense
}

func (a AdUnit) Enabled() bool { return a.Client != "" && a.Slot != "" }

// New returns just the handler (tests use this).
func New(cfg config.Config) (*gin.Engine, error) {
	app, err := NewApp(cfg)
	if err != nil {
		return nil, err
	}
	return app.Handler, nil
}

func NewApp(cfg config.Config) (*App, error) {
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	s := &Server{cfg: cfg, online: newOnline(cfg.BaseURL)}
	var err error
	if s.assetVersion, err = hashAssets(); err != nil {
		return nil, err
	}
	if err = s.loadTemplates(); err != nil {
		return nil, err
	}

	r := gin.New()
	if err = r.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	if cfg.IsProduction() {
		// Behind Fly's proxy every request comes from the proxy; Fly puts the
		// visitor's address in this header. Used for per-IP rate limits.
		r.TrustedPlatform = "Fly-Client-IP"
	}
	r.Use(gin.Recovery(), gin.LoggerWithConfig(gin.LoggerConfig{SkipPaths: []string{"/healthz"}}), s.securityHeaders())
	if cfg.IsProduction() {
		r.Use(s.canonicalHost())
	}

	// Not in Go's built-in table, and nosniff means browsers need the real type.
	if err = mime.AddExtensionType(".woff2", "font/woff2"); err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}
	// Asset URLs carry a content hash (?v=), so they can be cached forever.
	static := r.Group("/static", cacheControl("public, max-age=31536000, immutable"))
	static.StaticFS("/", http.FS(filesOnly{staticFS}))

	// Pages answer HEAD as well as GET: some crawlers and link checkers probe with HEAD.
	// (net/http drops the body for HEAD responses.)
	getHead := func(p string, h gin.HandlerFunc) {
		r.GET(p, h)
		r.HEAD(p, h)
	}
	getHead("/", s.page("home",
		"Tap Timer — Can you stop the clock at exactly 5 seconds?",
		"A free online timing game. The clock is hidden, so tap when you think 5 seconds have passed. Play solo or pass the phone with friends."))
	getHead("/party", s.page("party",
		"Party Mode — The pass-the-phone timer game | Tap Timer",
		"Pass one phone around your group. Everyone tries to hit the same goal time with the clock hidden. Closest wins."))
	getHead("/how-to-play", s.page("how-to-play",
		"How to Play Tap Timer — Rules, Modes & Tips",
		"Rules for Tap Timer's Goal Challenge, Tournament and Teams modes, plus tips for getting better at judging time."))
	getHead("/privacy", s.page("privacy",
		"Website Privacy Policy | Tap Timer",
		"How the Tap Timer website handles your information: browser storage, online rooms, server logs and Google ads."))
	getHead("/online", s.page("online",
		"Play Tap Timer Online with Friends: Multiplayer Timer Game",
		"Play the hidden-clock timer game online with friends, each on your own phone. Share a room code; closest to the goal over the match wins."))
	getHead("/r/:code", s.roomPage)
	r.GET("/r/:code/qr.svg", s.online.qrSVG)
	r.POST("/api/rooms", s.online.createRoom)
	r.GET("/ws/:code", s.online.socket)

	r.GET("/get", s.getApp)
	getHead("/ads.txt", s.adsTxt)
	getHead("/robots.txt", s.robotsTxt)
	getHead("/sitemap.xml", s.sitemap)
	// Browsers and iOS request these at the root regardless of <link> tags.
	r.GET("/favicon.ico", staticFile("static/img/favicon-32.png", "image/png"))
	r.GET("/apple-touch-icon.png", staticFile("static/img/apple-touch-icon.png", "image/png"))
	r.GET("/healthz", healthz)
	r.HEAD("/healthz", healthz)

	r.NoRoute(func(c *gin.Context) {
		s.render(c, http.StatusNotFound, "404", s.pageData(c, "404", "Page not found | Tap Timer", ""))
	})
	return &App{Handler: r, Close: s.online.Close, online: s.online}, nil
}

func (s *Server) loadTemplates() error {
	funcs := template.FuncMap{
		"asset": func(p string) string { return "/static/" + p + "?v=" + s.assetVersion },
		"appStoreURL": s.appStoreURL,
		"ad": func(name string) AdUnit {
			return AdUnit{
				Name:        name,
				Client:      s.cfg.AdSenseClient,
				Slot:        s.cfg.AdSlots[name],
				Placeholder: !s.cfg.IsProduction(),
			}
		},
	}
	base, err := template.New("").Funcs(funcs).ParseFS(web.FS, "templates/layout.html", "templates/partials/*.html")
	if err != nil {
		return err
	}
	pageFiles, err := fs.Glob(web.FS, "templates/pages/*.html")
	if err != nil {
		return err
	}
	s.pages = make(map[string]*template.Template, len(pageFiles))
	for _, f := range pageFiles {
		t, err := template.Must(base.Clone()).ParseFS(web.FS, f)
		if err != nil {
			return err
		}
		name := path.Base(f)
		s.pages[name[:len(name)-len(".html")]] = t
	}
	return nil
}

// render executes into a buffer first so a template error never sends a half-written page.
func (s *Server) render(c *gin.Context, status int, page string, data PageData) {
	t, ok := s.pages[page]
	if !ok {
		slog.Error("unknown page template", "page", page)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("render failed", "page", page, "err", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	c.Data(status, "text/html; charset=utf-8", buf.Bytes())
}

func hashAssets() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(web.FS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := web.FS.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10], err
}

func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		if s.cfg.IsProduction() {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		c.Next()
	}
}

// canonicalHost 301-redirects requests on any other host (www., the hosting
// platform's default domain, a bare IP) to BASE_URL, so search engines only
// ever see one copy of the site. Health checks are exempt because platforms
// probe them on internal hostnames.
func (s *Server) canonicalHost() gin.HandlerFunc {
	base, _ := url.Parse(s.cfg.BaseURL) // validated at startup
	return func(c *gin.Context) {
		if c.Request.Host == base.Host || c.Request.URL.Path == "/healthz" {
			c.Next()
			return
		}
		c.Redirect(http.StatusMovedPermanently, s.cfg.BaseURL+c.Request.URL.RequestURI())
		c.Abort()
	}
}

func cacheControl(v string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", v)
		c.Next()
	}
}

// filesOnly hides directories so the static handler never renders listings.
type filesOnly struct{ fs.FS }

func (f filesOnly) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := file.Stat(); err != nil || st.IsDir() {
		file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}

// staticFile serves one embedded file at a fixed, unversioned URL.
func staticFile(name, contentType string) gin.HandlerFunc {
	b, err := web.FS.ReadFile(name)
	if err != nil {
		panic(err) // embedded at build time; missing means a broken build
	}
	return func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=86400")
		c.Data(http.StatusOK, contentType, b)
	}
}

func healthz(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusOK, "ok")
}
