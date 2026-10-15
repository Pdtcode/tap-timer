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

	"taptimer/internal/i18n"
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
	Breadcrumb  string // this page's name in the structured-data breadcrumb; empty on Home

	Lang       i18n.Lang
	Alternates []Alternate       // this page in every language, for hreflang and the picker
	JS         map[string]string // app.js strings in this language
	Ads        *AdRotation
}

// Alternate is one language version of the current page.
type Alternate struct {
	Lang    i18n.Lang
	URL     string
	Current bool
}

// sitePages are the indexable pages: English path, template, and the key of
// their meta.<key>.title/.desc and crumb.<key> strings.
var sitePages = []struct{ path, page, key string }{
	{"/", "home", "home"},
	{"/party", "party", "party"},
	{"/online", "online", "online"},
	{"/how-to-play", "how-to-play", "howto"},
	{"/privacy", "privacy", "privacy"},
	{"/terms", "terms", "terms"},
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

var srcRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// pageData is the template data for page in language l. key picks the
// page's strings: meta.<key>.title, meta.<key>.desc and (inner pages) crumb.<key>.
func (s *Server) pageData(c *gin.Context, l i18n.Lang, page, key string) PageData {
	_, enPath := i18n.FromPath(c.Request.URL.Path)
	alts := make([]Alternate, len(i18n.Langs))
	for i, al := range i18n.Langs {
		alts[i] = Alternate{Lang: al, URL: s.cfg.BaseURL + al.Path(enPath), Current: al.Code == l.Code}
	}
	desc := ""
	if s.i18n.Has(i18n.Default.Code, "meta."+key+".desc") {
		desc = s.i18n.T(l.Code, "meta."+key+".desc")
	}
	crumb := ""
	if key != "home" && s.i18n.Has(i18n.Default.Code, "crumb."+key) {
		crumb = s.i18n.T(l.Code, "crumb."+key)
	}
	return PageData{
		Page:        page,
		Title:       s.i18n.T(l.Code, "meta."+key+".title"),
		Description: desc,
		Canonical:   s.cfg.BaseURL + c.Request.URL.Path,
		Breadcrumb:  crumb,
		Lang:        l,
		Alternates:  alts,
		JS:          s.i18n.JS(l.Code),
		Year:        time.Now().Year(),
		Ads:         NewAdRotation(),
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

func (s *Server) page(l i18n.Lang, name, key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s.render(c, http.StatusOK, l, name, s.pageData(c, l, name, key))
	}
}

// roomPage serves /r/:code (and /es/r/:code...), the link players share: the
// online multiplayer page with that room's code filled in. Room links aren't indexed.
func (s *Server) roomPage(l i18n.Lang) gin.HandlerFunc {
	return func(c *gin.Context) { s.room(c, l) }
}

func (s *Server) room(c *gin.Context, l i18n.Lang) {
	code := strings.ToUpper(c.Param("code"))
	if !roomCodeRe.MatchString(code) {
		s.render(c, http.StatusNotFound, l, "404", s.pageData(c, l, "404", "404"))
		return
	}
	d := s.pageData(c, l, "online", "room")
	d.Title = s.i18n.T(l.Code, "meta.room.title", "code", code)
	d.NoIndex = true
	d.RoomCode = code
	c.Header("X-Robots-Tag", "noindex")
	s.render(c, http.StatusOK, l, "online", d)
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
	c.String(http.StatusOK, "User-agent: *\nAllow: /\nDisallow: /get\nDisallow: /studio\n\nSitemap: %s/sitemap.xml\n", s.cfg.BaseURL)
}

// sitemap lists every page in every language, each with links to its other
// language versions (hreflang), as Google recommends for multilingual sites.
func (s *Server) sitemap(c *gin.Context) {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")
	esc := func(v string) string {
		var e strings.Builder
		xml.EscapeText(&e, []byte(v))
		return e.String()
	}
	for _, p := range sitePages {
		for _, l := range i18n.Langs {
			b.WriteString("  <url>\n    <loc>" + esc(s.cfg.BaseURL+l.Path(p.path)) + "</loc>\n")
			for _, al := range i18n.Langs {
				b.WriteString(`    <xhtml:link rel="alternate" hreflang="` + al.Hreflang + `" href="` + esc(s.cfg.BaseURL+al.Path(p.path)) + `"/>` + "\n")
			}
			b.WriteString(`    <xhtml:link rel="alternate" hreflang="x-default" href="` + esc(s.cfg.BaseURL+p.path) + `"/>` + "\n")
			b.WriteString("  </url>\n")
		}
	}
	b.WriteString("</urlset>\n")
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/xml; charset=utf-8", []byte(b.String()))
}
