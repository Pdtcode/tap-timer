package platform

import (
	"context"
	"time"

	"golang.org/x/oauth2"
)

// metaGraph is the Graph API version every Instagram call uses.
const metaGraph = "v25.0"

// Instagram posts Reels through the Instagram Graph API. It needs a
// Business or Creator account linked to a Facebook Page, and the resumable
// (streamed) upload only works with Facebook Login for Business.
//
// Meta's tokens don't refresh: the code exchanges for a long-lived token
// (about 60 days), and you reconnect and save the new one before it runs out.
type Instagram struct {
	oauth *oauth2.Config
}

func NewInstagram(c Credentials) *Instagram {
	return &Instagram{oauth: &oauth2.Config{
		ClientID:     c.ID,
		ClientSecret: c.Secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://www.facebook.com/" + metaGraph + "/dialog/oauth",
			TokenURL:  "https://graph.facebook.com/" + metaGraph + "/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes: []string{
			"instagram_basic",
			"instagram_content_publish",
			"instagram_manage_insights",
			"pages_show_list",
			"business_management",
		},
	}}
}

func (i *Instagram) Name() string     { return "instagram" }
func (i *Instagram) Label() string    { return "Instagram Reels" }
func (i *Instagram) Src() string      { return "ig_reels" }
func (i *Instagram) Configured() bool { return i.oauth.ClientID != "" && i.oauth.ClientSecret != "" }

func (i *Instagram) AuthURL(state, redirectURL string) string {
	c := *i.oauth
	c.RedirectURL = redirectURL
	return c.AuthCodeURL(state)
}

// Exchange trades the code for a short-lived token, then (TODO) for a
// long-lived one: GET /oauth/access_token?grant_type=fb_exchange_token&fb_exchange_token=<token>.
func (i *Instagram) Exchange(ctx context.Context, code, redirectURL string) (*oauth2.Token, error) {
	c := *i.oauth
	c.RedirectURL = redirectURL
	return c.Exchange(ctx, code)
}

// TokenSource returns the stored token as is: Meta tokens can't be refreshed.
func (i *Instagram) TokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	return oauth2.StaticTokenSource(tok)
}

// The long-lived access token is what we keep (INSTAGRAM_ACCESS_TOKEN).
func (i *Instagram) TokenEnv() string                    { return "INSTAGRAM_ACCESS_TOKEN" }
func (i *Instagram) SavedToken(tok *oauth2.Token) string { return tok.AccessToken }
func (i *Instagram) RestoreToken(saved string) *oauth2.Token {
	return &oauth2.Token{AccessToken: saved}
}

// Account finds the Instagram account behind the Page:
// GET /me/accounts?fields=instagram_business_account{id,username}
func (i *Instagram) Account(ctx context.Context, ts oauth2.TokenSource) (Account, error) {
	return Account{}, ErrNotImplemented
}

// Upload creates a Reels container, streams the bytes to Meta and publishes:
//  1. POST https://graph.facebook.com/v25.0/<ig-user-id>/media with media_type=REELS,
//     upload_type=resumable, caption, share_to_feed=true. The response has the container id.
//  2. POST https://rupload.facebook.com/ig-api-upload/v25.0/<container-id> with headers
//     Authorization: OAuth <token>, offset: 0, file_size: v.Size, and the bytes as the body.
//  3. Poll GET /<container-id>?fields=status_code until FINISHED (Meta processes the video),
//     then POST /<ig-user-id>/media_publish?creation_id=<container-id>.
//  4. GET /<media-id>?fields=permalink for the URL.
func (i *Instagram) Upload(ctx context.Context, ts oauth2.TokenSource, v Video) (Result, error) {
	return Result{}, ErrNotImplemented
}

// History: GET /<ig-user-id>/media?fields=id,caption,permalink,timestamp,media_type,like_count,comments_count
// (views come from GET /<media-id>/insights?metric=views, one call per Reel).
func (i *Instagram) History(ctx context.Context, ts oauth2.TokenSource, since time.Time) ([]Post, error) {
	return nil, ErrNotImplemented
}
