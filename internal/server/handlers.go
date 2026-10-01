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
var sitemapPaths = []string{"/", "/party", "/how-to-play", "/privacy"}

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

// getApp is the single exit point to the App Store. Every CTA links here with
// ?src=<placement> so clicks are logged and, when a provider token is set,
// installs are attributed per placement in App Store Connect.
func (s *Server) getApp(c *gin.Context) {
	src := c.Query("src")
	if !srcRe.MatchString(src) {
		src = "unknown"
	}

	q := url.Values{}
	if s.cfg.AppStoreProviderToken != "" {
		q.Set("pt", s.cfg.AppStoreProviderToken)
		q.Set("ct", "web-"+src)
	}
	q.Set("mt", "8")
	target := url.URL{
		Scheme:   "https",
		Host:     "apps.apple.com",
		Path:     "/app/apple-store/id" + s.cfg.AppStoreID,
		RawQuery: q.Encode(),
	}

	slog.Info("app_store_click", "src", src, "ua", c.Request.UserAgent(), "referer", c.Request.Referer())
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")
	c.Redirect(http.StatusFound, target.String())
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
