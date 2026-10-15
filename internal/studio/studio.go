// Package studio is /studio: a private, phone-friendly dashboard for posting
// one vertical video to YouTube Shorts, Instagram Reels and TikTok at once,
// with caption templates, a daily streak and per-post stats.
//
// It has no database. Videos stream through to the platforms; each
// platform's login is a secret you save once after connecting; history,
// the streak and stats are read back from the platforms themselves.
package studio

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"taptimer/internal/config"
	"taptimer/internal/studio/platform"
	"taptimer/web"
)

type Studio struct {
	cfg       config.Config
	loc       *time.Location
	signer    *signer
	platforms []platform.Platform
	conns     map[string]*conn // by platform name
	pages     map[string]*template.Template
	limiter   *loginLimiter
	history   historyCache
	// secretsDir is where local (non-production) runs save logins.
	secretsDir string
}

// conn is one platform's login, in memory.
type conn struct {
	mu      sync.Mutex
	ts      oauth2.TokenSource // nil when not connected
	saved   bool               // came from a saved secret, so it survives restarts
	account *platform.Account  // looked up once per login
}

func (c *conn) tokenSource() oauth2.TokenSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ts
}

// New wires up the platforms and restores saved logins. funcs must provide
// "asset" (the site's versioned static URLs).
func New(cfg config.Config, funcs template.FuncMap) (*Studio, error) {
	sc := cfg.Studio
	loc, err := time.LoadLocation(sc.TZ)
	if err != nil {
		return nil, err
	}
	s := &Studio{
		cfg:    cfg,
		loc:    loc,
		signer: newSigner(sc.Secret),
		platforms: platform.All(
			platform.Credentials{ID: sc.YouTubeClientID, Secret: sc.YouTubeClientSecret},
			platform.Credentials{ID: sc.MetaAppID, Secret: sc.MetaAppSecret},
			platform.Credentials{ID: sc.TikTokClientKey, Secret: sc.TikTokClientSecret},
		),
		conns:      map[string]*conn{},
		limiter:    &loginLimiter{fails: map[string][]time.Time{}},
		secretsDir: config.SecretsDir,
	}
	for _, p := range s.platforms {
		c := &conn{}
		if saved := sc.Tokens[p.TokenEnv()]; saved != "" && p.Configured() {
			c.ts = p.TokenSource(context.Background(), p.RestoreToken(saved))
			c.saved = true
		}
		s.conns[p.Name()] = c
	}
	if err := s.loadTemplates(funcs); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Studio) Register(r *gin.Engine) {
	g := r.Group("/studio", noIndex, s.sameOrigin)
	g.GET("/manifest.webmanifest", manifest) // fetched without cookies, so outside the sign-in
	g.GET("/login", s.loginPage)
	g.POST("/login", s.login)
	g.POST("/logout", s.logout)

	a := g.Group("", s.requireAuth)
	a.GET("", s.dashboard)
	a.GET("/new", s.newPost)
	a.GET("/record", s.record)
	a.GET("/connections", s.connections)
	a.GET("/connect/:platform", s.connect)
	a.GET("/connect/:platform/callback", s.connectCallback)
	a.POST("/api/upload", s.upload)
}

func (s *Studio) platform(name string) platform.Platform {
	for _, p := range s.platforms {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func (s *Studio) redirectURL(p platform.Platform) string {
	return s.cfg.BaseURL + "/studio/connect/" + p.Name() + "/callback"
}

// bioLink is the tracked link to put in each platform's profile bio.
func (s *Studio) bioLink(p platform.Platform) string {
	return s.cfg.BaseURL + "/get?src=" + p.Src()
}

// account looks up (once) who a platform's login belongs to.
func (s *Studio) account(ctx context.Context, p platform.Platform) (*platform.Account, error) {
	c := s.conns[p.Name()]
	c.mu.Lock()
	ts, acct := c.ts, c.account
	c.mu.Unlock()
	if ts == nil || acct != nil {
		return acct, nil
	}
	a, err := p.Account(ctx, ts)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.account = &a
	c.mu.Unlock()
	return &a, nil
}

// historyCache keeps what the platforms said for a couple of minutes, so
// the dashboard is quick and light on API quota.
type historyCache struct {
	mu      sync.Mutex
	at      time.Time
	entries []entry
	errs    map[string]string // platform name -> error, for the dashboard
}

const historyTTL = 2 * time.Minute

// entry is a post on one platform.
type entry struct {
	Platform string
	platform.Post
}

// recentPosts returns your posts on every connected platform from the last
// year, newest first.
func (s *Studio) recentPosts(ctx context.Context, now time.Time) ([]entry, map[string]string) {
	h := &s.history
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.at.IsZero() && now.Sub(h.at) < historyTTL {
		return h.entries, h.errs
	}
	var (
		all  []entry
		errs = map[string]string{}
	)
	for _, p := range s.platforms {
		ts := s.conns[p.Name()].tokenSource()
		if ts == nil {
			continue
		}
		posts, err := p.History(ctx, ts, now.AddDate(-1, 0, 0))
		if err != nil {
			if !errors.Is(err, platform.ErrNotImplemented) {
				slog.Warn("studio: history", "platform", p.Name(), "err", err)
				errs[p.Name()] = err.Error()
			}
			continue
		}
		for _, post := range posts {
			all = append(all, entry{Platform: p.Name(), Post: post})
		}
	}
	sortNewestFirst(all)
	h.at, h.entries, h.errs = now, all, errs
	return all, errs
}

func (s *Studio) forgetHistory() {
	s.history.mu.Lock()
	s.history.at = time.Time{}
	s.history.mu.Unlock()
}

func noIndex(c *gin.Context) {
	c.Header("X-Robots-Tag", "noindex, nofollow")
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func (s *Studio) loadTemplates(funcs template.FuncMap) error {
	all := template.FuncMap{
		"localTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(s.loc).Format("Mon Jan 2, 3:04 PM")
		},
		"label": func(name string) string {
			if p := s.platform(name); p != nil {
				return p.Label()
			}
			return name
		},
	}
	for k, v := range funcs {
		all[k] = v
	}
	base, err := template.New("").Funcs(all).ParseFS(web.FS, "templates/studio/layout.html", "templates/studio/partials/*.html")
	if err != nil {
		return err
	}
	files, err := fs.Glob(web.FS, "templates/studio/pages/*.html")
	if err != nil {
		return err
	}
	s.pages = make(map[string]*template.Template, len(files))
	for _, f := range files {
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
func (s *Studio) render(c *gin.Context, status int, page string, data gin.H) {
	t, ok := s.pages[page]
	if !ok {
		slog.Error("studio: unknown page template", "page", page)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	data["Page"] = page
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("studio: render failed", "page", page, "err", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	c.Data(status, "text/html; charset=utf-8", buf.Bytes())
}
