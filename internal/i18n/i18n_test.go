package i18n

import (
	"html/template"
	"regexp"
	"slices"
	"testing"
	"testing/fstest"

	"taptimer/web"
)

func TestPaths(t *testing.T) {
	es := Langs[1]
	for _, tc := range []struct {
		in       string
		lang     string
		englishP string
	}{
		{"/", "en", "/"},
		{"/party", "en", "/party"},
		{"/es", "es", "/"},
		{"/es/", "es", "/"},
		{"/es/party", "es", "/party"},
		{"/pt/r/K7QX", "pt-BR", "/r/K7QX"},
		{"/estonia", "en", "/estonia"}, // only whole segments count
	} {
		l, p := FromPath(tc.in)
		if l.Code != tc.lang || p != tc.englishP {
			t.Errorf("FromPath(%q) = %s, %q; want %s, %q", tc.in, l.Code, p, tc.lang, tc.englishP)
		}
	}
	if es.Path("/") != "/es/" || es.Path("/party") != "/es/party" || Default.Path("/party") != "/party" {
		t.Error("Path")
	}
}

func TestTranslate(t *testing.T) {
	c, err := Load(fstest.MapFS{
		"i/en.json": {Data: []byte(`{"hi": "Hi {name}", "only_en": "English", "rich": "<b>{x}</b>"}`)},
		"i/es.json": {Data: []byte(`{"hi": "Hola {name}"}`)},
	}, "i")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.T("es", "hi", "name", "Ana"); got != "Hola Ana" {
		t.Errorf("T = %q", got)
	}
	if got := c.T("es", "only_en"); got != "English" {
		t.Errorf("fallback = %q", got)
	}
	if got := c.T("es", "nope"); got != "nope" {
		t.Errorf("unknown key = %q", got)
	}
	if got := c.HTML("en", "rich", "x", "<script>"); got != template.HTML("<b>&lt;script&gt;</b>") {
		t.Errorf("HTML escapes values: %q", got)
	}
	if _, err := Load(fstest.MapFS{
		"i/en.json": {Data: []byte(`{"a": "A"}`)},
		"i/de.json": {Data: []byte(`{"typo": "B"}`)},
	}, "i"); err == nil {
		t.Error("a key English doesn't have should fail")
	}
}

var (
	placeholderRe = regexp.MustCompile(`\{[a-z_]+\}`)
	tagRe         = regexp.MustCompile(`</?([a-z]+)`)
)

func sorted(re *regexp.Regexp, s string, group int) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[group])
	}
	slices.Sort(out)
	return out
}

// The real catalogs: complete, and every translation keeps English's
// placeholders and HTML tags (a renamed {goal} or a lost </a> breaks the page).
func TestCatalogs(t *testing.T) {
	c, err := Load(web.FS, "i18n")
	if err != nil {
		t.Fatal(err)
	}
	en := c.msgs[Default.Code]
	for _, l := range Langs[1:] {
		if missing := c.Missing(l.Code); len(missing) > 0 {
			t.Errorf("%s is missing %d strings, e.g. %v", l.Code, len(missing), missing[:min(5, len(missing))])
		}
		for k, v := range c.msgs[l.Code] {
			if a, b := sorted(placeholderRe, en[k], 0), sorted(placeholderRe, v, 0); !slices.Equal(a, b) {
				t.Errorf("%s %s: placeholders %v, English has %v", l.Code, k, b, a)
			}
			if a, b := sorted(tagRe, en[k], 1), sorted(tagRe, v, 1); !slices.Equal(a, b) {
				t.Errorf("%s %s: tags %v, English has %v", l.Code, k, b, a)
			}
		}
		if !c.Has(l.Code, "layout.app_english") {
			t.Errorf("%s: layout.app_english should say the app is in English", l.Code)
		}
	}
}
