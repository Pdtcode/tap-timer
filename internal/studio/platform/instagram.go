package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	// metaGraph is the Graph API version every Instagram call uses.
	metaGraph   = "v25.0"
	graphAPI    = "https://graph.facebook.com/" + metaGraph
	reelsUpload = "https://rupload.facebook.com/ig-api-upload/" + metaGraph

	igCaptionMax = 2200 // characters
	// History reads at most this many pages of 50 posts, and asks for views
	// (one call per Reel) only for the newest igViewsFor.
	igMaxPages = 5
	igViewsFor = 25
	// Meta processes a Reel after the upload; give up waiting after this.
	igProcessTimeout = 10 * time.Minute
)

// Instagram posts Reels through the Instagram Graph API. It needs a
// Business or Creator account linked to a Facebook Page, and the resumable
// (streamed) upload only works with Facebook Login for Business.
//
// Meta's tokens don't refresh: the code exchanges for a long-lived token
// (about 60 days), and you reconnect and save the new one before it runs out.
// The Accounts page shows when that is.
type Instagram struct {
	oauth *oauth2.Config
	// configID is the Facebook Login for Business configuration, which Meta
	// recommends over a scope list. Empty means ask for the scopes below.
	configID string
	// Overridden in tests.
	graphBase, uploadBase string
	pollEvery             time.Duration
}

func NewInstagram(c Credentials) *Instagram {
	return &Instagram{
		oauth: &oauth2.Config{
			ClientID:     c.ID,
			ClientSecret: c.Secret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   "https://www.facebook.com/" + metaGraph + "/dialog/oauth",
				TokenURL:  graphAPI + "/oauth/access_token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			Scopes: []string{
				"instagram_basic",
				"instagram_content_publish",
				"instagram_manage_insights", // views
				"pages_show_list",
				"pages_read_engagement", // the Page's linked Instagram account
				"business_management",   // Pages owned through a business portfolio
			},
		},
		configID:   c.LoginConfig,
		graphBase:  graphAPI,
		uploadBase: reelsUpload,
		pollEvery:  3 * time.Second,
	}
}

func (i *Instagram) Name() string     { return "instagram" }
func (i *Instagram) Label() string    { return "Instagram Reels" }
func (i *Instagram) Src() string      { return "ig_reels" }
func (i *Instagram) Configured() bool { return i.oauth.ClientID != "" && i.oauth.ClientSecret != "" }

func (i *Instagram) AuthURL(state, redirectURL string) string {
	c := *i.oauth
	c.RedirectURL = redirectURL
	if i.configID != "" {
		c.Scopes = nil // the configuration lists the permissions
		return c.AuthCodeURL(state,
			oauth2.SetAuthURLParam("config_id", i.configID),
			oauth2.SetAuthURLParam("override_default_response_type", "true"))
	}
	return c.AuthCodeURL(state)
}

// Exchange trades the code for a short-lived token (about an hour), then
// that for a long-lived one (about 60 days), which is what we keep.
func (i *Instagram) Exchange(ctx context.Context, code, redirectURL string) (*oauth2.Token, error) {
	c := *i.oauth
	c.RedirectURL = redirectURL
	short, err := c.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {i.oauth.ClientID},
		"client_secret":     {i.oauth.ClientSecret},
		"fb_exchange_token": {short.AccessToken},
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := i.call(ctx, http.DefaultClient, http.MethodGet, "/oauth/access_token?"+q.Encode(), nil, &body); err != nil {
		return nil, fmt.Errorf("long-lived token: %w", err)
	}
	tok := &oauth2.Token{AccessToken: body.AccessToken, TokenType: "Bearer"}
	if body.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// TokenSource returns the stored token as is: Meta tokens can't be refreshed.
// The expiry is dropped so an expired token reaches Meta, whose error says so.
func (i *Instagram) TokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok.AccessToken, TokenType: "Bearer"})
}

// The long-lived access token is what we keep (INSTAGRAM_ACCESS_TOKEN).
func (i *Instagram) TokenEnv() string                    { return "INSTAGRAM_ACCESS_TOKEN" }
func (i *Instagram) SavedToken(tok *oauth2.Token) string { return tok.AccessToken }
func (i *Instagram) RestoreToken(saved string) *oauth2.Token {
	return &oauth2.Token{AccessToken: saved}
}

// Account finds the Instagram account linked to the first Page you manage
// that has one, and when the login runs out.
func (i *Instagram) Account(ctx context.Context, ts oauth2.TokenSource) (Account, error) {
	acct, err := i.igUser(ctx, ts)
	if err != nil {
		return Account{}, err
	}
	acct.Expires = i.expiry(ctx, ts)
	return acct, nil
}

func (i *Instagram) igUser(ctx context.Context, ts oauth2.TokenSource) (Account, error) {
	var body struct {
		Data []struct {
			Name string `json:"name"`
			IG   *struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"instagram_business_account"`
		} `json:"data"`
	}
	path := "/me/accounts?fields=name,instagram_business_account{id,username}&limit=100"
	if err := i.call(ctx, oauth2.NewClient(ctx, ts), http.MethodGet, path, nil, &body); err != nil {
		return Account{}, err
	}
	for _, page := range body.Data {
		if page.IG != nil && page.IG.ID != "" {
			return Account{ID: page.IG.ID, Handle: "@" + page.IG.Username}, nil
		}
	}
	return Account{}, errors.New("instagram: no Instagram professional account is linked to a Facebook Page you manage (or it wasn't ticked when connecting)")
}

// expiry asks Meta when the token runs out. Zero means never, or unknown.
func (i *Instagram) expiry(ctx context.Context, ts oauth2.TokenSource) time.Time {
	tok, err := ts.Token()
	if err != nil {
		return time.Time{}
	}
	q := url.Values{
		"input_token":  {tok.AccessToken},
		"access_token": {i.oauth.ClientID + "|" + i.oauth.ClientSecret}, // the app token
	}
	var body struct {
		Data struct {
			ExpiresAt int64 `json:"expires_at"`
		} `json:"data"`
	}
	if i.call(ctx, http.DefaultClient, http.MethodGet, "/debug_token?"+q.Encode(), nil, &body) != nil || body.Data.ExpiresAt == 0 {
		return time.Time{}
	}
	return time.Unix(body.Data.ExpiresAt, 0)
}

// Upload creates a Reels container, streams the bytes to Meta, waits for
// Meta to process the video, then publishes it.
func (i *Instagram) Upload(ctx context.Context, ts oauth2.TokenSource, v Video) (Result, error) {
	acct, err := i.igUser(ctx, ts)
	if err != nil {
		return Result{}, err
	}
	tok, err := ts.Token()
	if err != nil {
		return Result{}, err
	}
	client := oauth2.NewClient(ctx, ts)

	// 1. The container: where the Reel's settings live until it's published.
	form := url.Values{
		"media_type":    {"REELS"},
		"upload_type":   {"resumable"},
		"caption":       {igText(v.Caption, igCaptionMax)},
		"share_to_feed": {"true"},
	}
	var container struct {
		ID string `json:"id"`
	}
	if err := i.call(ctx, client, http.MethodPost, "/"+acct.ID+"/media", form, &container); err != nil {
		return Result{}, fmt.Errorf("create container: %w", err)
	}

	// 2. The bytes, streamed in one request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.uploadBase+"/"+container.ID, v.Body)
	if err != nil {
		return Result{}, err
	}
	req.ContentLength = v.Size // the body is a pipe, so net/http can't tell
	req.Header.Set("Authorization", "OAuth "+tok.AccessToken)
	req.Header.Set("offset", "0")
	req.Header.Set("file_size", strconv.FormatInt(v.Size, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("instagram: upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, ruploadError(resp)
	}

	// 3. Meta transcodes the video before it can be published.
	if err := i.waitProcessed(ctx, client, container.ID); err != nil {
		return Result{}, err
	}

	// 4. Publish, then look up the post's link.
	var published struct {
		ID string `json:"id"`
	}
	if err := i.call(ctx, client, http.MethodPost, "/"+acct.ID+"/media_publish", url.Values{"creation_id": {container.ID}}, &published); err != nil {
		return Result{}, fmt.Errorf("publish: %w", err)
	}
	var media struct {
		Permalink string `json:"permalink"`
	}
	if err := i.call(ctx, client, http.MethodGet, "/"+published.ID+"?fields=permalink", nil, &media); err != nil {
		media.Permalink = "" // it's posted; the link is a nicety
	}
	return Result{RemoteID: published.ID, URL: media.Permalink, Status: StatusPublished}, nil
}

// waitProcessed polls the container until Meta has processed the video.
func (i *Instagram) waitProcessed(ctx context.Context, client *http.Client, id string) error {
	deadline := time.Now().Add(igProcessTimeout)
	for {
		var c struct {
			StatusCode string `json:"status_code"` // IN_PROGRESS, FINISHED, ERROR, EXPIRED, PUBLISHED
			Status     string `json:"status"`      // a sentence; says why on ERROR
		}
		if err := i.call(ctx, client, http.MethodGet, "/"+id+"?fields=status_code,status", nil, &c); err != nil {
			return fmt.Errorf("processing: %w", err)
		}
		switch c.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			return fmt.Errorf("instagram: processing failed: %s", orElse(c.Status, c.StatusCode))
		}
		if time.Now().After(deadline) {
			return errors.New("instagram: still processing after 10 minutes; check the app, it may post later")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(i.pollEvery):
		}
	}
}

// History lists your Reels since a time, newest first, with likes,
// comments and (for the newest ones) views.
func (i *Instagram) History(ctx context.Context, ts oauth2.TokenSource, since time.Time) ([]Post, error) {
	acct, err := i.igUser(ctx, ts)
	if err != nil {
		return nil, err
	}
	client := oauth2.NewClient(ctx, ts)
	var posts []Post
	next := "/" + acct.ID + "/media?fields=id,caption,permalink,timestamp,media_product_type,like_count,comments_count&limit=50"
	for range igMaxPages {
		var body struct {
			Data []struct {
				ID        string `json:"id"`
				Caption   string `json:"caption"`
				Permalink string `json:"permalink"`
				Timestamp string `json:"timestamp"` // 2026-10-14T15:00:00+0000
				Product   string `json:"media_product_type"`
				Likes     int64  `json:"like_count"`
				Comments  int64  `json:"comments_count"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := i.call(ctx, client, http.MethodGet, next, nil, &body); err != nil {
			return nil, err
		}
		done := body.Paging.Next == ""
		for _, m := range body.Data {
			at, _ := time.Parse("2006-01-02T15:04:05-0700", m.Timestamp)
			if at.Before(since) {
				done = true
				continue
			}
			if m.Product != "REELS" {
				continue
			}
			posts = append(posts, Post{
				ID: m.ID, Title: firstLine(m.Caption), URL: m.Permalink, PostedAt: at,
				Privacy: "public", Stats: Stats{Likes: m.Likes, Comments: m.Comments},
			})
		}
		if done {
			break
		}
		next = body.Paging.Next // a full URL
	}
	i.fillViews(ctx, client, posts[:min(len(posts), igViewsFor)])
	return posts, nil
}

// fillViews asks for each Reel's views, a few at a time. A Reel whose
// insights aren't ready yet just shows 0.
func (i *Instagram) fillViews(ctx context.Context, client *http.Client, posts []Post) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for k := range posts {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			var body struct {
				Data []struct {
					Values []struct {
						Value int64 `json:"value"`
					} `json:"values"`
				} `json:"data"`
			}
			if i.call(ctx, client, http.MethodGet, "/"+posts[k].ID+"/insights?metric=views", nil, &body) == nil &&
				len(body.Data) > 0 && len(body.Data[0].Values) > 0 {
				posts[k].Stats.Views = body.Data[0].Values[0].Value
			}
		})
	}
	wg.Wait()
}

// call sends one Graph API request: path is relative to the API version
// (or a full URL from a paging link), and form, if set, is the POST body.
func (i *Instagram) call(ctx context.Context, client *http.Client, method, path string, form url.Values, into any) error {
	target := path
	if !strings.HasPrefix(path, "http") {
		target = i.graphBase + path
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("instagram: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return graphError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// graphError turns Meta's error JSON into a message worth showing, e.g.
// "instagram: Error validating access token: Session has expired (code 190)".
func graphError(resp *http.Response) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			UserMsg string `json:"error_user_msg"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &body) != nil || body.Error.Message == "" {
		return fmt.Errorf("instagram: %s", resp.Status)
	}
	msg := orElse(body.Error.UserMsg, body.Error.Message)
	if body.Error.Code == 190 { // the login is no longer valid
		msg += ". Reconnect Instagram on the Accounts page and save the new login"
	}
	return fmt.Errorf("instagram: %s (code %d)", msg, body.Error.Code)
}

// ruploadError reads the upload host's error, which has its own shape.
func ruploadError(resp *http.Response) error {
	var body struct {
		DebugInfo struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"debug_info"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &body) != nil || body.DebugInfo.Message == "" {
		return fmt.Errorf("instagram: upload: %s", resp.Status)
	}
	return fmt.Errorf("instagram: upload: %s: %s", body.DebugInfo.Type, body.DebugInfo.Message)
}

// igText trims a caption to Instagram's length limit in characters.
func igText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

// firstLine is a caption's first line, as the post's title on the dashboard.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return igText(line, 100)
}

// orElse returns the first non-empty string.
func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
