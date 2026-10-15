package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"taptimer/internal/studio/platform"
)

const (
	maxVideoBytes = 500 << 20 // phone clips are far smaller; Reels caps at 300 MB anyway
	uploadTimeout = 30 * time.Minute
	recentShown   = 20
	calendarDays  = 28
)

// manifest makes "Add to Home Screen" a standalone app whose scope is all of
// /studio, so moving between tabs stays inside it instead of opening Safari.
func manifest(c *gin.Context) {
	c.Header("Content-Type", "application/manifest+json")
	c.String(http.StatusOK, `{
  "name": "Tap Timer Studio",
  "short_name": "Studio",
  "id": "/studio",
  "start_url": "/studio",
  "scope": "/studio",
  "display": "standalone",
  "background_color": "#040e08",
  "theme_color": "#040e08",
  "icons": [
    {"src": "/static/img/apple-touch-icon.png", "sizes": "180x180", "type": "image/png"},
    {"src": "/static/img/icon-128.png", "sizes": "128x128", "type": "image/png"}
  ]
}
`)
}

// --- login ---

func (s *Studio) loginPage(c *gin.Context) {
	s.render(c, http.StatusOK, "login", gin.H{"Title": "Sign in"})
}

func (s *Studio) login(c *gin.Context) {
	now, ip := time.Now(), c.ClientIP()
	if s.limiter.blocked(ip, now) {
		s.render(c, http.StatusTooManyRequests, "login", gin.H{"Title": "Sign in", "Error": "Too many tries. Wait 15 minutes."})
		return
	}
	if !s.passwordOK(c.PostForm("password")) {
		s.limiter.fail(ip, now)
		slog.Warn("studio: failed login", "ip", ip)
		s.render(c, http.StatusUnauthorized, "login", gin.H{"Title": "Sign in", "Error": "Wrong password."})
		return
	}
	s.setCookie(c, sessionCookie, s.newSession(now), int(sessionTTL.Seconds()))
	c.Redirect(http.StatusSeeOther, "/studio")
}

func (s *Studio) logout(c *gin.Context) {
	s.setCookie(c, sessionCookie, "", -1)
	c.Redirect(http.StatusSeeOther, "/studio/login")
}

// --- pages ---

type platformView struct {
	Name, Label, BioLink, TokenEnv string
	Configured                     bool
	Connected                      bool
	Saved                          bool // survives restarts
	Handle                         string
	Error                          string
}

func (s *Studio) platformViews(c *gin.Context, errs map[string]string) []platformView {
	out := make([]platformView, 0, len(s.platforms))
	for _, p := range s.platforms {
		cn := s.conns[p.Name()]
		cn.mu.Lock()
		v := platformView{
			Name: p.Name(), Label: p.Label(), BioLink: s.bioLink(p), TokenEnv: p.TokenEnv(),
			Configured: p.Configured(), Connected: cn.ts != nil, Saved: cn.saved,
			Error: errs[p.Name()],
		}
		cn.mu.Unlock()
		if v.Connected {
			acct, err := s.account(c.Request.Context(), p)
			switch {
			case err == nil && acct != nil:
				v.Handle = acct.Handle
			case err != nil && !errors.Is(err, platform.ErrNotImplemented) && v.Error == "":
				v.Error = err.Error()
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Studio) dashboard(c *gin.Context) {
	now := time.Now()
	posts, errs := s.recentPosts(c.Request.Context(), now)
	times := make([]time.Time, len(posts))
	for i, p := range posts {
		times[i] = p.PostedAt
	}
	cal := calendar(times, now, s.loc, calendarDays)
	s.render(c, http.StatusOK, "dashboard", gin.H{
		"Title":       "Studio",
		"Streak":      streak(times, now, s.loc),
		"PostedToday": cal[len(cal)-1].Posted,
		"Calendar":    cal,
		"Posts":       posts[:min(len(posts), recentShown)],
		"Platforms":   s.platformViews(c, errs),
	})
}

// postForm is what the post form (on New post and Record) needs.
func (s *Studio) postForm(c *gin.Context, title string) gin.H {
	// {link} is filled in here; the page fills {title} as you type.
	captions := map[string]string{}
	for _, p := range s.platforms {
		captions[p.Name()] = strings.ReplaceAll(defaultTemplates[p.Name()], "{link}", s.bioLink(p))
	}
	return gin.H{
		"Title":     title,
		"Platforms": s.platformViews(c, nil),
		"Captions":  captions,
		"MaxMB":     maxVideoBytes >> 20,
	}
}

func (s *Studio) newPost(c *gin.Context) {
	s.render(c, http.StatusOK, "new", s.postForm(c, "New post"))
}

// record plays the timer on a canvas with a facecam and records it in the
// browser (studio-record.js). Posting happens on New post, which picks the
// recording up from the browser's storage.
func (s *Studio) record(c *gin.Context) {
	s.render(c, http.StatusOK, "record", gin.H{"Title": "Record"})
}

func (s *Studio) connections(c *gin.Context) {
	flash := ""
	if name, outcome, ok := strings.Cut(c.Query("flash"), "-"); ok {
		if p := s.platform(name); p != nil {
			flash = map[string]string{
				"refused":      p.Label() + " sign-in was cancelled.",
				"failed":       "Couldn't connect " + p.Label() + ". Try again.",
				"unconfigured": p.Label() + " needs its developer app set up first.",
			}[outcome]
		}
	}
	s.render(c, http.StatusOK, "connections", gin.H{
		"Title":      "Accounts",
		"Platforms":  s.platformViews(c, nil),
		"Flash":      flash,
		"Production": s.cfg.IsProduction(),
	})
}

// --- OAuth ---

func (s *Studio) connect(c *gin.Context) {
	p := s.platform(c.Param("platform"))
	if p == nil {
		c.Status(http.StatusNotFound)
		return
	}
	if !p.Configured() {
		c.Redirect(http.StatusSeeOther, "/studio/connections?flash="+p.Name()+"-unconfigured")
		return
	}
	state := randomToken()
	s.setCookie(c, stateCookie, p.Name()+"."+state, 600)
	c.Redirect(http.StatusSeeOther, p.AuthURL(state, s.redirectURL(p)))
}

// connectCallback finishes connecting. The login works right away, but with
// no database it only survives a restart once it's saved: locally the studio
// writes it to secrets/; in production this page shows the secret to set.
func (s *Studio) connectCallback(c *gin.Context) {
	p := s.platform(c.Param("platform"))
	if p == nil {
		c.Status(http.StatusNotFound)
		return
	}
	want, _ := c.Cookie(stateCookie)
	s.setCookie(c, stateCookie, "", -1)
	if want == "" || want != p.Name()+"."+c.Query("state") {
		c.String(http.StatusBadRequest, "Sign-in expired or didn't match. Go back to Accounts and try again.")
		return
	}
	if e := c.Query("error"); e != "" {
		slog.Warn("studio: oauth refused", "platform", p.Name(), "error", e, "desc", c.Query("error_description"))
		c.Redirect(http.StatusSeeOther, "/studio/connections?flash="+p.Name()+"-refused")
		return
	}
	ctx := c.Request.Context()
	tok, err := p.Exchange(ctx, c.Query("code"), s.redirectURL(p))
	if err != nil {
		slog.Error("studio: oauth exchange", "platform", p.Name(), "err", err)
		c.Redirect(http.StatusSeeOther, "/studio/connections?flash="+p.Name()+"-failed")
		return
	}

	cn := s.conns[p.Name()]
	cn.mu.Lock()
	// Not the request's context: refreshes happen long after this request.
	cn.ts = p.TokenSource(context.Background(), tok)
	cn.saved, cn.account = false, nil
	cn.mu.Unlock()
	s.forgetHistory()

	saved := p.SavedToken(tok)
	data := gin.H{"Title": "Connected", "Label": p.Label(), "TokenEnv": p.TokenEnv(), "Token": saved}
	if acct, err := s.account(ctx, p); err == nil && acct != nil {
		data["Handle"] = acct.Handle
	}
	if !s.cfg.IsProduction() {
		file := filepath.Join(s.secretsDir, p.TokenEnv())
		if err := os.WriteFile(file, []byte(saved+"\n"), 0o600); err != nil {
			slog.Error("studio: save login locally", "file", file, "err", err)
		} else {
			cn.mu.Lock()
			cn.saved = true
			cn.mu.Unlock()
			data["SavedTo"] = file
		}
	}
	s.render(c, http.StatusOK, "connected", data)
}

// --- posting ---

// uploadMeta travels in the X-Studio-Post header (base64url JSON), because
// the body is the video itself.
type uploadMeta struct {
	Title    string            `json:"title"`
	Captions map[string]string `json:"captions"` // platform -> final caption; its keys pick the platforms
}

type uploadResult struct {
	Platform string          `json:"platform"`
	Label    string          `json:"label"`
	Status   platform.Status `json:"status"`
	URL      string          `json:"url,omitempty"`
	Error    string          `json:"error,omitempty"`
}

func parseUploadMeta(header string) (uploadMeta, error) {
	var m uploadMeta
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(header, "="))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return m, errors.New("bad X-Studio-Post header")
	}
	m.Title = strings.TrimSpace(m.Title)
	if m.Title == "" || len([]rune(m.Title)) > 100 { // YouTube's title limit
		return m, errors.New("title must be 1–100 characters")
	}
	if len(m.Captions) == 0 {
		return m, errors.New("pick at least one platform")
	}
	return m, nil
}

// upload streams the request body (the raw video file) to every chosen
// platform at the same time, without saving it, and publishes it.
// Content-Length must be set: every platform needs the size up front.
func (s *Studio) upload(c *gin.Context) {
	meta, err := parseUploadMeta(c.GetHeader("X-Studio-Post"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	size := c.Request.ContentLength
	switch {
	case size <= 0:
		c.JSON(http.StatusLengthRequired, gin.H{"error": "Content-Length required"})
		return
	case size > maxVideoBytes:
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("video is over %d MB", maxVideoBytes>>20)})
		return
	}
	// The server's 10s/15s timeouts suit pages, not a phone uploading a video.
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetReadDeadline(time.Now().Add(uploadTimeout)); err != nil {
		slog.Warn("studio: read deadline", "err", err)
	}
	if err := rc.SetWriteDeadline(time.Now().Add(uploadTimeout + time.Minute)); err != nil {
		slog.Warn("studio: write deadline", "err", err)
	}

	ctx := c.Request.Context()
	var (
		results []uploadResult
		sinks   []func(io.Reader) error
		sinkFor []int // results index of each sink
	)
	for _, p := range s.platforms { // display order
		caption, ok := meta.Captions[p.Name()]
		if !ok {
			continue
		}
		r := uploadResult{Platform: p.Name(), Label: p.Label()}
		ts := s.conns[p.Name()].tokenSource()
		if ts == nil {
			r.Status, r.Error = platform.StatusFailed, p.Label()+" isn't connected"
			results = append(results, r)
			continue
		}
		i := len(results)
		results = append(results, r)
		sinkFor = append(sinkFor, i)
		v := platform.Video{Size: size, Title: meta.Title, Caption: strings.TrimSpace(caption)}
		sinks = append(sinks, func(body io.Reader) error {
			v.Body = body
			res, err := p.Upload(ctx, ts, v)
			results[i].Status, results[i].URL = res.Status, res.URL
			return err
		})
	}
	errs := fanout(io.LimitReader(c.Request.Body, size), sinks)
	for j, err := range errs {
		if err != nil {
			r := &results[sinkFor[j]]
			r.Status, r.Error = platform.StatusFailed, err.Error()
			slog.Warn("studio: upload failed", "platform", r.Platform, "err", err)
		}
	}
	s.forgetHistory()

	status := http.StatusOK
	if slices.ContainsFunc(results, func(r uploadResult) bool { return r.Status == platform.StatusFailed }) {
		status = http.StatusMultiStatus // some platforms failed; the page shows which
	}
	c.JSON(status, gin.H{"results": results})
}

func sortNewestFirst(es []entry) {
	slices.SortStableFunc(es, func(a, b entry) int { return b.PostedAt.Compare(a.PostedAt) })
}
