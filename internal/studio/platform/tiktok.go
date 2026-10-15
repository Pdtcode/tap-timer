package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	tiktokAuthURL  = "https://www.tiktok.com/v2/auth/authorize/"
	tiktokTokenURL = "https://open.tiktokapis.com/v2/oauth/token/"
)

// TikTok posts through the Content Posting API. Its OAuth calls the client
// id "client_key", so it doesn't go through oauth2.Config.
//
// A post either goes up when it's uploaded (Direct Post) or lands in your
// TikTok inbox as a draft that you finish in the app, where you can also add
// a trending sound. Until TikTok audits the app, Direct Post can only post
// privately.
type TikTok struct {
	creds Credentials
	http  *http.Client
}

func NewTikTok(c Credentials) *TikTok {
	return &TikTok{creds: c, http: &http.Client{Timeout: 30 * time.Second}}
}

func (t *TikTok) Name() string     { return "tiktok" }
func (t *TikTok) Label() string    { return "TikTok" }
func (t *TikTok) Src() string      { return "tiktok" }
func (t *TikTok) Configured() bool { return t.creds.ID != "" && t.creds.Secret != "" }

var tiktokScopes = []string{"user.info.basic", "video.upload", "video.publish", "video.list"}

func (t *TikTok) AuthURL(state, redirectURL string) string {
	q := url.Values{
		"client_key":    {t.creds.ID},
		"response_type": {"code"},
		"scope":         {strings.Join(tiktokScopes, ",")},
		"redirect_uri":  {redirectURL},
		"state":         {state},
	}
	return tiktokAuthURL + "?" + q.Encode()
}

func (t *TikTok) Exchange(ctx context.Context, code, redirectURL string) (*oauth2.Token, error) {
	return t.token(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURL},
	})
}

func (t *TikTok) TokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(tok, tiktokRefresher{ctx: ctx, t: t, refresh: tok.RefreshToken})
}

type tiktokRefresher struct {
	ctx     context.Context
	t       *TikTok
	refresh string
}

func (r tiktokRefresher) Token() (*oauth2.Token, error) {
	return r.t.token(r.ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r.refresh}})
}

// token calls the token endpoint (code exchange or refresh).
func (t *TikTok) token(ctx context.Context, form url.Values) (*oauth2.Token, error) {
	form.Set("client_key", t.creds.ID)
	form.Set("client_secret", t.creds.Secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tiktokTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		OpenID       string `json:"open_id"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("tiktok token: %s: %w", resp.Status, err)
	}
	if body.Error != "" || body.AccessToken == "" {
		return nil, fmt.Errorf("tiktok token: %s %s", body.Error, body.Description)
	}
	tok := &oauth2.Token{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}
	return tok.WithExtra(map[string]any{"open_id": body.OpenID}), nil
}

// The refresh token is what we keep (TIKTOK_REFRESH_TOKEN; valid for a year).
// TODO: check whether TikTok's refresh hands back a new refresh token and
// retires the old one; if so the saved secret goes stale and you'd reconnect.
func (t *TikTok) TokenEnv() string                        { return "TIKTOK_REFRESH_TOKEN" }
func (t *TikTok) SavedToken(tok *oauth2.Token) string     { return tok.RefreshToken }
func (t *TikTok) RestoreToken(saved string) *oauth2.Token { return &oauth2.Token{RefreshToken: saved} }

// Account: GET https://open.tiktokapis.com/v2/user/info/?fields=open_id,display_name,username
func (t *TikTok) Account(ctx context.Context, ts oauth2.TokenSource) (Account, error) {
	return Account{}, ErrNotImplemented
}

// Upload starts a FILE_UPLOAD post and PUTs the bytes in chunks:
//  1. Draft (the default for now, since it works before the audit):
//     POST https://open.tiktokapis.com/v2/post/publish/inbox/video/init/
//     Direct Post: POST .../v2/post/publish/video/init/ with post_info
//     {title: caption, privacy_level} (privacy_level must be one that
//     /v2/post/publish/creator_info/query/ allows).
//     Both send source_info {source:"FILE_UPLOAD", video_size, chunk_size, total_chunk_count}.
//  2. PUT each chunk to upload_url with Content-Range: bytes <first>-<last>/<size>.
//     Chunks are 5–64 MB (the last may be up to 128 MB), so this buffers one chunk in memory.
//  3. Poll POST /v2/post/publish/status/fetch/ with publish_id for the outcome.
func (t *TikTok) Upload(ctx context.Context, ts oauth2.TokenSource, v Video) (Result, error) {
	return Result{}, ErrNotImplemented
}

// History: POST https://open.tiktokapis.com/v2/video/list/?fields=id,title,share_url,create_time,view_count,like_count,comment_count
// (20 per page, newest first). Drafts don't appear until you post them.
func (t *TikTok) History(ctx context.Context, ts oauth2.TokenSource, since time.Time) ([]Post, error) {
	return nil, ErrNotImplemented
}
