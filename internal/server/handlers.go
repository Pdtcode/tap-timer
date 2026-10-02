package server

import (
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// PageData is the root object every page template receives.
type PageData struct {
	Page        string
	Title       string
	Description string
	Canonical   string
	Year        int
	Site        SiteData
	NoIndex     bool   // keep out of search results (e.g. room links)
	RoomCode    string // the room a /r/:code link opens
}

type SiteData struct {
	BaseURL       string
	AppStoreID    string
	AdSenseClient string
	GAID          string
	ContactEmail  string

	GoogleSiteVerification string
	BingSiteVerification   string
}

// Pages listed in the sitemap.
var sitemapPaths = []string{"/", "/party", "/online", "/how-to-play", "/privacy"}

var srcRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func (s *Server) pageData(c *gin.Context, page, title, desc string) PageData {
	return PageData{
		Page:        page,
		Title:       title,
		Description: desc,
		Canonical:   s.cfg.BaseURL + c.Request.URL.Path,
		Year:        time.Now().Year(),
		Site: SiteData{
			BaseURL:       s.cfg.BaseURL,
			AppStoreID:    s.cfg.AppStoreID,
			AdSenseClient: s.cfg.AdSenseClient,
			GAID:          s.cfg.GAMeasurementID,
			ContactEmail:  s.cfg.ContactEmail,

			GoogleSiteVerification: s.cfg.GoogleSiteVerification,
			BingSiteVerification:   s.cfg.BingSiteVerification,
		},
	}
}

func (s *Server) page(name, title, desc string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s.render(c, http.StatusOK, name, s.pageData(c, name, title, desc))
	}
}

// roomPage serves /r/:code, the link players share: the online multiplayer page
// with that room's code filled in. Room links aren't indexed.
func (s *Server) roomPage(c *gin.Context) {
	code := strings.ToUpper(c.Param("code"))
	if !roomCodeRe.MatchString(code) {
		s.render(c, http.StatusNotFound, "404", s.pageData(c, "404", "Page not found | Tap Timer", ""))
		return
	}
	d := s.pageData(c, "online",
		"Join room "+code+" | Play Tap Timer Online",
		"You've been invited to a Tap Timer room. Stop the hidden clock closest to the goal to win.")
	d.NoIndex = true
	d.RoomCode = code
	c.Header("X-Robots-Tag", "noindex")
	s.render(c, http.StatusOK, "online", d)
}

// getApp is the single exit point to the App Store. Every CTA links here with
// ?src=<placement> so clicks are logged and, when a provider token is set,
// installs are attributed per placement in App Store Connect.
func (s *Server) getApp(c *gin.Context) {
	src := c.Query("src")
	if !srcRe.MatchString(src) {
		src = "unknown"
	}
	slog.Info("app_store_click", "src", src, "ua", c.Request.UserAgent(), "referer", c.Request.Referer())
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")
	c.Redirect(http.StatusFound, s.appStoreURL(src))
}

// appStoreURL is the App Store page with campaign tags for src, so installs
// are attributed per placement in App Store Connect when a provider token is
// set. Shared messages use it directly (no hop through /get), since they're
// opened on other people's phones.
func (s *Server) appStoreURL(src string) string {
	q := url.Values{}
	if s.cfg.AppStoreProviderToken != "" {
		q.Set("pt", s.cfg.AppStoreProviderToken)
		q.Set("ct", "web-"+src)
	}
	q.Set("mt", "8")
	u := url.URL{
		Scheme:   "https",
		Host:     "apps.apple.com",
		Path:     "/app/apple-store/id" + s.cfg.AppStoreID,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func (s *Server) adsTxt(c *gin.Context) {
	if s.cfg.AdSenseClient == "" {
		c.String(http.StatusNotFound, "not found")
		return
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.String(http.StatusOK, "google.com, %s, DIRECT, f08c47fec0942fa0\n", s.cfg.AdSensePublisherID())
}

func (s *Server) robotsTxt(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.String(http.StatusOK, "User-agent: *\nAllow: /\nDisallow: /get\n\nSitemap: %s/sitemap.xml\n", s.cfg.BaseURL)
}

func (s *Server) sitemap(c *gin.Context) {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range sitemapPaths {
		b.WriteString("  <url><loc>")
		xml.EscapeText(&b, []byte(s.cfg.BaseURL+p))
		b.WriteString("</loc></url>\n")
	}
	b.WriteString("</urlset>\n")
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/xml; charset=utf-8", []byte(b.String()))
}
