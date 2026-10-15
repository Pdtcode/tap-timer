// Package server wires up the Gin router, templates and handlers.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"

	"taptimer/internal/config"
	"taptimer/internal/i18n"
	"taptimer/internal/studio"
	"taptimer/web"
)

type Server struct {
	cfg          config.Config
	i18n         *i18n.Catalog
	pages        map[string]map[string]*template.Template // lang code -> page -> template
	assetVersion string
	online       *online
	studio       *studio.Studio // nil unless STUDIO_PASSWORD is set
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
	if s.i18n, err = i18n.Load(web.FS, "i18n"); err != nil {
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
	// Compress text responses (app.js and app.css shrink to roughly a quarter).
	// Not WebSockets, which need the raw connection, nor already-compressed
	// files, nor the studio API, whose video upload extends its own deadlines.
	r.Use(gzip.Gzip(gzip.DefaultCompression,
		gzip.WithExcludedPaths([]string{"/ws/", "/studio/api/"}),
		gzip.WithExcludedExtensions([]string{".png", ".woff2"})))
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
	// Every page in every language: English at the root, the others under
	// their prefix (/es/party). Titles and descriptions are in web/i18n
	// (meta.<key>.title/.desc); see sitePages.
	for _, l := range i18n.Langs {
		for _, p := range sitePages {
			getHead(l.Path(p.path), s.page(l, p.page, p.key))
		}
		getHead(l.Path("/r/:code"), s.roomPage(l))
		r.GET(l.Path("/r/:code/qr.svg"), s.online.qrSVG(l.Prefix))
	}
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

	if cfg.Studio.Enabled() {
		// A studio problem must not take the game down with it: log it and
		// serve the site without /studio.
		if st, err := studio.New(cfg, template.FuncMap{"asset": s.asset}); err != nil {
			slog.Error("studio off: it failed to start", "err", err)
		} else {
			s.studio = st
			s.studio.Register(r)
			slog.Info("studio on", "url", cfg.BaseURL+"/studio")
		}
	} else {
		slog.Info("studio off: STUDIO_PASSWORD isn't set")
	}

	r.NoRoute(func(c *gin.Context) {
		l, _ := i18n.FromPath(c.Request.URL.Path)
		s.render(c, http.StatusNotFound, l, "404", s.pageData(c, l, "404", "404"))
	})
	return &App{Handler: r, Close: s.online.Close, online: s.online}, nil
}

// loadTemplates parses every page once per language, with "t", "th" and
// "link" bound to that language. Pages that are mostly prose (how to play,
// privacy, terms) are translated as whole files in templates/pages/i18n/<code>/;
// the rest use keys from web/i18n.
func (s *Server) loadTemplates() error {
	pageFiles, err := fs.Glob(web.FS, "templates/pages/*.html")
	if err != nil {
		return err
	}
	s.pages = make(map[string]map[string]*template.Template, len(i18n.Langs))
	for _, l := range i18n.Langs {
		code := l.Code
		funcs := template.FuncMap{
			"asset":       s.asset,
			"appStoreURL": s.appStoreURL,
			"ad": func(name string) AdUnit {
				return AdUnit{
					Name:        name,
					Client:      s.cfg.AdSenseClient,
					Slot:        s.cfg.AdSlots[name],
					Placeholder: !s.cfg.IsProduction(),
				}
			},
			"t":    func(key string, pairs ...any) string { return s.i18n.T(code, key, pairs...) },
			"th":   func(key string, pairs ...any) template.HTML { return s.i18n.HTML(code, key, pairs...) },
			"has":  func(key string) bool { return s.i18n.Has(code, key) },
			"link": l.Path,
		}
		base, err := template.New("").Funcs(funcs).ParseFS(web.FS, "templates/layout.html", "templates/partials/*.html")
		if err != nil {
			return err
		}
		s.pages[code] = make(map[string]*template.Template, len(pageFiles))
		for _, f := range pageFiles {
			name := path.Base(f)
			if !l.IsDefault() {
				if alt := path.Join("templates/pages/i18n", code, name); fileExists(alt) {
					f = alt
				}
			}
			t, err := template.Must(base.Clone()).ParseFS(web.FS, f)
			if err != nil {
				return fmt.Errorf("%s (%s): %w", f, code, err)
			}
			s.pages[code][name[:len(name)-len(".html")]] = t
		}
	}
	return nil
}

func fileExists(name string) bool {
	_, err := fs.Stat(web.FS, name)
	return err == nil
}

// render executes into a buffer first so a template error never sends a half-written page.
func (s *Server) render(c *gin.Context, status int, l i18n.Lang, page string, data PageData) {
	t, ok := s.pages[l.Code][page]
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

func (s *Server) asset(p string) string { return "/static/" + p + "?v=" + s.assetVersion }

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
