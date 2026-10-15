// Package i18n holds the site's languages and translated strings.
//
// English is the default and lives at the root (/party); every other language
// has its own path prefix (/es/party), so each version can rank on its own
// and hreflang tags can point between them. Strings come from
// web/i18n/<code>.json: flat keys, {name} placeholders. A missing string falls
// back to English. Keys starting with "js." are also sent to app.js.
package i18n

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"sort"
	"strings"
)

type Lang struct {
	Code     string // catalog file and html lang, e.g. "pt-BR"
	Prefix   string // URL prefix, "" for English
	Hreflang string
	OGLocale string // Open Graph locale, e.g. "pt_BR"
	Name     string // in its own language, for the picker
}

// Langs is every language the site is published in, English first.
var Langs = []Lang{
	{Code: "en", Prefix: "", Hreflang: "en", OGLocale: "en_US", Name: "English"},
	{Code: "es", Prefix: "/es", Hreflang: "es", OGLocale: "es_ES", Name: "Español"},
	{Code: "pt-BR", Prefix: "/pt", Hreflang: "pt-BR", OGLocale: "pt_BR", Name: "Português (Brasil)"},
	{Code: "de", Prefix: "/de", Hreflang: "de", OGLocale: "de_DE", Name: "Deutsch"},
	{Code: "id", Prefix: "/id", Hreflang: "id", OGLocale: "id_ID", Name: "Bahasa Indonesia"},
}

// Default is English.
var Default = Langs[0]

func (l Lang) IsDefault() bool { return l.Prefix == "" }

// Path is p (an English path like "/" or "/party") in this language.
func (l Lang) Path(p string) string {
	if l.Prefix == "" {
		return p
	}
	if p == "/" {
		return l.Prefix + "/"
	}
	return l.Prefix + p
}

// FromPath finds the language of a request path and returns it with the
// English path ("/es/party" → es, "/party"; "/es" → es, "/").
func FromPath(p string) (Lang, string) {
	for _, l := range Langs[1:] {
		if p == l.Prefix || p == l.Prefix+"/" {
			return l, "/"
		}
		if strings.HasPrefix(p, l.Prefix+"/") {
			return l, p[len(l.Prefix):]
		}
	}
	return Default, p
}

type Catalog struct {
	msgs map[string]map[string]string // lang code -> key -> text
}

// Load reads <dir>/<code>.json for every language. English must exist; a
// translation may not have keys English doesn't (that's always a typo).
func Load(fsys fs.FS, dir string) (*Catalog, error) {
	c := &Catalog{msgs: map[string]map[string]string{}}
	for _, l := range Langs {
		b, err := fs.ReadFile(fsys, path.Join(dir, l.Code+".json"))
		if err != nil {
			if l.IsDefault() {
				return nil, err
			}
			c.msgs[l.Code] = map[string]string{}
			continue
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s.json: %w", l.Code, err)
		}
		c.msgs[l.Code] = m
	}
	for code, m := range c.msgs {
		for k := range m {
			if _, ok := c.msgs[Default.Code][k]; !ok {
				return nil, fmt.Errorf("%s.json has %q, which en.json doesn't", code, k)
			}
		}
	}
	return c, nil
}

// T is key in lang with {name} placeholders filled from name/value pairs.
func (c *Catalog) T(lang, key string, pairs ...any) string {
	s, ok := c.msgs[lang][key]
	if !ok || s == "" {
		s, ok = c.msgs[Default.Code][key]
	}
	if !ok {
		return key // shows up in tests and on screen, never silently empty
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(pairs[i])+"}", fmt.Sprint(pairs[i+1]))
	}
	return s
}

// HTML is T for strings that contain markup (our own, trusted). Values are
// escaped unless they're already template.HTML.
func (c *Catalog) HTML(lang, key string, pairs ...any) template.HTML {
	esc := make([]any, len(pairs))
	for i, v := range pairs {
		switch h, isHTML := v.(template.HTML); {
		case i%2 == 0: // a placeholder name
			esc[i] = v
		case isHTML:
			esc[i] = string(h)
		default:
			esc[i] = template.HTMLEscapeString(fmt.Sprint(v))
		}
	}
	return template.HTML(c.T(lang, key, esc...))
}

// JS is every "js." string for lang (English filling gaps), without the prefix.
func (c *Catalog) JS(lang string) map[string]string {
	out := map[string]string{}
	for k := range c.msgs[Default.Code] {
		if name, ok := strings.CutPrefix(k, "js."); ok {
			out[name] = c.T(lang, k)
		}
	}
	return out
}

// Missing lists English keys a language hasn't translated, sorted.
func (c *Catalog) Missing(lang string) []string {
	var out []string
	for k, v := range c.msgs[Default.Code] {
		if v == "" {
			continue // intentionally empty in English (e.g. a note only other languages need)
		}
		if c.msgs[lang][k] == "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Has reports whether key has a non-empty string in lang, without falling
// back to English (for notes that only some languages show).
func (c *Catalog) Has(lang, key string) bool { return c.msgs[lang][key] != "" }
