package platform

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeMeta stands in for the Graph API and the Reels upload host.
type fakeMeta struct {
	t         *testing.T
	srv       *httptest.Server
	container url.Values // the create-container form
	uploaded  []byte
	polls     int
	finalCode string // what the container ends up as (FINISHED by default)
	published bool
}

func newFakeMeta(t *testing.T) (*fakeMeta, *Instagram) {
	f := &fakeMeta{t: t, finalCode: "FINISHED"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	i := NewInstagram(Credentials{ID: "app", Secret: "shh"})
	i.graphBase, i.uploadBase, i.pollEvery = f.srv.URL+"/graph", f.srv.URL+"/rupload", time.Millisecond
	i.oauth.Endpoint.TokenURL = f.srv.URL + "/graph/oauth/access_token"
	return f, i
}

func (f *fakeMeta) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/json")
	// The token calls carry their credentials in the query or form instead.
	switch {
	case r.URL.Path == "/graph/oauth/access_token" && r.Method == http.MethodPost:
		r.ParseForm()
		if r.PostForm.Get("code") != "the-code" {
			f.t.Errorf("code = %q", r.PostForm.Get("code"))
		}
		io.WriteString(w, `{"access_token":"short","token_type":"bearer","expires_in":3600}`)
		return
	case r.URL.Path == "/graph/oauth/access_token":
		if q.Get("grant_type") != "fb_exchange_token" || q.Get("fb_exchange_token") != "short" || q.Get("client_secret") != "shh" {
			f.t.Errorf("long-lived exchange query = %q", r.URL.RawQuery)
		}
		io.WriteString(w, `{"access_token":"long","token_type":"bearer","expires_in":5184000}`)
		return
	case r.URL.Path == "/graph/debug_token":
		if q.Get("input_token") != "tok" || q.Get("access_token") != "app|shh" {
			f.t.Errorf("debug_token query = %q", r.URL.RawQuery)
		}
		io.WriteString(w, `{"data":{"is_valid":true,"expires_at":1798761600}}`)
		return
	case r.URL.Path == "/rupload/c1":
		if r.Header.Get("Authorization") != "OAuth tok" || r.Header.Get("offset") != "0" ||
			r.Header.Get("file_size") != "11" || r.ContentLength != 11 {
			f.t.Errorf("rupload headers = %v, length %d", r.Header, r.ContentLength)
		}
		f.uploaded, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"success":true,"message":"Upload successful."}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Error validating access token: Session has expired","code":190}}`)
		return
	}
	switch {
	case r.URL.Path == "/graph/me/accounts":
		io.WriteString(w, `{"data":[{"name":"No IG"},{"name":"Tap Timer","instagram_business_account":{"id":"ig1","username":"taptimer"}}]}`)
	case r.URL.Path == "/graph/ig1/media" && r.Method == http.MethodPost:
		r.ParseForm()
		f.container = r.PostForm
		io.WriteString(w, `{"id":"c1","uri":"https://rupload.facebook.com/ig-api-upload/v25.0/c1"}`)
	case r.URL.Path == "/graph/c1":
		f.polls++
		code := "IN_PROGRESS"
		if f.polls >= 2 {
			code = f.finalCode
		}
		io.WriteString(w, `{"status_code":"`+code+`","status":"Error: video too short","id":"c1"}`)
	case r.URL.Path == "/graph/ig1/media_publish":
		r.ParseForm()
		if r.PostForm.Get("creation_id") != "c1" {
			f.t.Errorf("creation_id = %q", r.PostForm.Get("creation_id"))
		}
		f.published = true
		io.WriteString(w, `{"id":"m1"}`)
	case r.URL.Path == "/graph/m1":
		io.WriteString(w, `{"permalink":"https://www.instagram.com/reel/abc/","id":"m1"}`)
	case r.URL.Path == "/graph/ig1/media":
		item := func(id, at, product string) string {
			return `{"id":"` + id + `","caption":"Hit ` + id + `\n\nFree to try","permalink":"https://www.instagram.com/reel/` + id + `/",` +
				`"timestamp":"` + at + `","media_product_type":"` + product + `","like_count":4,"comments_count":1}`
		}
		if q.Get("after") == "" {
			next := f.srv.URL + "/graph/ig1/media?after=p2"
			io.WriteString(w, `{"data":[`+item("a", "2026-10-14T15:00:00+0000", "REELS")+`,`+item("photo", "2026-10-13T15:00:00+0000", "FEED")+`],
				"paging":{"next":"`+next+`"}}`)
		} else {
			// b is in range, c is older than since, so paging stops here.
			io.WriteString(w, `{"data":[`+item("b", "2026-10-12T15:00:00+0000", "REELS")+`,`+item("c", "2026-09-01T15:00:00+0000", "REELS")+`],
				"paging":{"next":"`+f.srv.URL+`/graph/ig1/media?after=p3"}}`)
		}
	case r.URL.Path == "/graph/a/insights":
		if q.Get("metric") != "views" {
			f.t.Errorf("insights metric = %q", q.Get("metric"))
		}
		io.WriteString(w, `{"data":[{"name":"views","values":[{"value":321}]}]}`)
	case r.URL.Path == "/graph/b/insights":
		w.WriteHeader(http.StatusBadRequest) // not ready yet: shows 0
		io.WriteString(w, `{"error":{"message":"not ready","code":100}}`)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func TestInstagramUpload(t *testing.T) {
	f, i := newFakeMeta(t)
	res, err := i.Upload(t.Context(), testTS, Video{
		Body: strings.NewReader("video bytes"), Size: 11, Title: "ignored", Caption: "  Hit 10.00\n\n#taptimer  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteID != "m1" || res.URL != "https://www.instagram.com/reel/abc/" || res.Status != StatusPublished {
		t.Errorf("result = %+v", res)
	}
	if string(f.uploaded) != "video bytes" {
		t.Errorf("uploaded %q", f.uploaded)
	}
	c := f.container
	if c.Get("media_type") != "REELS" || c.Get("upload_type") != "resumable" || c.Get("share_to_feed") != "true" ||
		c.Get("caption") != "Hit 10.00\n\n#taptimer" {
		t.Errorf("container form = %v", c)
	}
	if f.polls != 2 || !f.published {
		t.Errorf("polls = %d, published = %v", f.polls, f.published)
	}
}

func TestInstagramUploadProcessingFails(t *testing.T) {
	f, i := newFakeMeta(t)
	f.finalCode = "ERROR"
	_, err := i.Upload(t.Context(), testTS, Video{Body: strings.NewReader("video bytes"), Size: 11})
	if err == nil || !strings.Contains(err.Error(), "processing failed: Error: video too short") {
		t.Errorf("err = %v", err)
	}
	if f.published {
		t.Error("published a failed container")
	}
}

func TestInstagramExpiredLogin(t *testing.T) {
	_, i := newFakeMeta(t)
	_, err := i.History(t.Context(), oauthTS("old"), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "Session has expired") || !strings.Contains(err.Error(), "Reconnect Instagram") {
		t.Errorf("err = %v", err)
	}
}

func TestInstagramAccountAndHistory(t *testing.T) {
	_, i := newFakeMeta(t)
	acct, err := i.Account(t.Context(), testTS)
	if err != nil || acct.ID != "ig1" || acct.Handle != "@taptimer" || acct.Expires.Unix() != 1798761600 {
		t.Errorf("account = %+v, %v", acct, err)
	}
	posts, err := i.History(t.Context(), testTS, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %+v", posts)
	}
	a, b := posts[0], posts[1]
	if a.ID != "a" || a.Title != "Hit a" || a.URL != "https://www.instagram.com/reel/a/" || a.Stats != (Stats{321, 4, 1}) ||
		!a.PostedAt.Equal(time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("a = %+v", a)
	}
	if b.ID != "b" || b.Stats != (Stats{Likes: 4, Comments: 1}) {
		t.Errorf("b = %+v", b)
	}
}

func TestInstagramExchangeLongLived(t *testing.T) {
	_, i := newFakeMeta(t)
	tok, err := i.Exchange(t.Context(), "the-code", "https://tap-timer.com/studio/connect/instagram/callback")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "long" || time.Until(tok.Expiry) < 59*24*time.Hour {
		t.Errorf("token = %+v", tok)
	}
	if i.RestoreToken(i.SavedToken(tok)).AccessToken != "long" {
		t.Error("saved token doesn't round-trip")
	}
}

func TestInstagramAuthURL(t *testing.T) {
	i := NewInstagram(Credentials{ID: "app", Secret: "shh"})
	u, _ := url.Parse(i.AuthURL("st", "https://x/cb"))
	if q := u.Query(); !strings.Contains(q.Get("scope"), "instagram_content_publish") || q.Get("config_id") != "" {
		t.Errorf("scopes URL = %s", u)
	}
	i = NewInstagram(Credentials{ID: "app", Secret: "shh", LoginConfig: "cfg9"})
	u, _ = url.Parse(i.AuthURL("st", "https://x/cb"))
	if q := u.Query(); q.Get("config_id") != "cfg9" || q.Has("scope") || q.Get("response_type") != "code" || q.Get("state") != "st" {
		t.Errorf("config URL = %s", u)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("\n Nailed 5.00s \nmore"); got != "Nailed 5.00s" {
		t.Errorf("firstLine = %q", got)
	}
}

func oauthTS(access string) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: access})
}
