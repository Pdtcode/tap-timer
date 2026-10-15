package platform

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeYouTube stands in for the upload and data APIs.
type fakeYouTube struct {
	t        *testing.T
	srv      *httptest.Server
	meta     ytVideo
	uploaded []byte
	initErr  string // JSON error body for the first call, if set
}

func newFakeYouTube(t *testing.T) (*fakeYouTube, *YouTube) {
	f := &fakeYouTube{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	y := NewYouTube(Credentials{ID: "id", Secret: "secret"})
	y.apiBase, y.uploadBase = f.srv.URL+"/api", f.srv.URL+"/upload"
	return f, y
}

func (f *fakeYouTube) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/upload/videos":
		if f.initErr != "" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, f.initErr)
			return
		}
		if r.URL.Query().Get("uploadType") != "resumable" || r.Header.Get("X-Upload-Content-Length") != "11" {
			f.t.Errorf("init: query %q, length %q", r.URL.RawQuery, r.Header.Get("X-Upload-Content-Length"))
		}
		json.NewDecoder(r.Body).Decode(&f.meta)
		w.Header().Set("Location", f.srv.URL+"/session/abc")
	case r.Method == http.MethodPut && r.URL.Path == "/session/abc":
		if r.ContentLength != 11 {
			f.t.Errorf("PUT Content-Length = %d, want 11", r.ContentLength)
		}
		f.uploaded, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"id":"vid123","status":{"uploadStatus":"uploaded"}}`)
	case r.URL.Path == "/api/channels":
		io.WriteString(w, `{"items":[{"id":"UC1","snippet":{"title":"Tap Timer","customUrl":"@taptimer"},
			"contentDetails":{"relatedPlaylists":{"uploads":"UU1"}}}]}`)
	case r.URL.Path == "/api/playlistItems":
		if r.URL.Query().Get("playlistId") != "UU1" {
			f.t.Errorf("playlistId = %q", r.URL.Query().Get("playlistId"))
		}
		item := func(id, at string) string {
			return `{"snippet":{"title":"t-` + id + `","publishedAt":"` + at + `","resourceId":{"videoId":"` + id + `"}}}`
		}
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"nextPageToken":"p2","items":[`+item("a", "2026-10-14T15:00:00Z")+`,`+item("b", "2026-10-13T15:00:00Z")+`]}`)
		} else {
			// c is older than since, so paging stops here.
			io.WriteString(w, `{"nextPageToken":"p3","items":[`+item("c", "2026-09-01T15:00:00Z")+`]}`)
		}
	case r.URL.Path == "/api/videos":
		if r.URL.Query().Get("id") != "a,b" || r.URL.Query().Get("part") != "status,statistics" {
			f.t.Errorf("videos query = %q", r.URL.RawQuery)
		}
		io.WriteString(w, `{"items":[{"id":"b","status":{"privacyStatus":"private"},"statistics":{"viewCount":"7"}},
			{"id":"a","status":{"privacyStatus":"public"},"statistics":{"viewCount":"1200","likeCount":"88","commentCount":"5"}}]}`)
	default:
		http.NotFound(w, r)
	}
}

var testTS = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})

func TestYouTubeUploadNow(t *testing.T) {
	f, y := newFakeYouTube(t)
	res, err := y.Upload(t.Context(), testTS, Video{
		Body: strings.NewReader("video bytes"), Size: 11,
		Title: "Hit <10.00>", Caption: "Free to try",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteID != "vid123" || res.Status != StatusPublished || res.URL != "https://youtube.com/shorts/vid123" {
		t.Errorf("result = %+v", res)
	}
	if string(f.uploaded) != "video bytes" {
		t.Errorf("uploaded %q", f.uploaded)
	}
	m := f.meta
	if m.Snippet.Title != "Hit ‹10.00›" || m.Snippet.Description != "Free to try" || m.Snippet.CategoryID != "20" {
		t.Errorf("snippet = %+v", m.Snippet)
	}
	if m.Status.PrivacyStatus != "public" || m.Status.SelfDeclaredMadeForKids {
		t.Errorf("status = %+v", m.Status)
	}
}

func TestYouTubeUploadError(t *testing.T) {
	f, y := newFakeYouTube(t)
	f.initErr = `{"error":{"message":"You have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`
	_, err := y.Upload(t.Context(), testTS, Video{Body: strings.NewReader("video bytes"), Size: 11, Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "quotaExceeded: You have exceeded your quota.") {
		t.Errorf("err = %v", err)
	}
}

func TestYouTubeAccountAndHistory(t *testing.T) {
	_, y := newFakeYouTube(t)
	acct, err := y.Account(t.Context(), testTS)
	if err != nil || acct.Handle != "@taptimer" || acct.ID != "UC1" {
		t.Errorf("account = %+v, %v", acct, err)
	}
	posts, err := y.History(t.Context(), testTS, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %+v", posts)
	}
	a, b := posts[0], posts[1]
	if a.ID != "a" || a.Title != "t-a" || a.URL != "https://youtube.com/shorts/a" || a.Privacy != "public" || a.Stats != (Stats{1200, 88, 5}) {
		t.Errorf("a = %+v", a)
	}
	if b.ID != "b" || b.Privacy != "private" || b.Stats != (Stats{Views: 7}) || !b.PostedAt.Equal(time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("b = %+v", b)
	}
}

func TestYouTubeSavedToken(t *testing.T) {
	y := NewYouTube(Credentials{})
	tok := y.RestoreToken(y.SavedToken(&oauth2.Token{AccessToken: "short-lived", RefreshToken: "keep-me"}))
	if tok.RefreshToken != "keep-me" || tok.AccessToken != "" {
		t.Errorf("restored %+v", tok)
	}
}

func TestYTText(t *testing.T) {
	if got := ytText("  "+strings.Repeat("é", 120)+"  ", 100); len([]rune(got)) != 100 {
		t.Errorf("len = %d runes", len([]rune(got)))
	}
}
