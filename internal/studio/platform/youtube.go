package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

const (
	youtubeAPI    = "https://www.googleapis.com/youtube/v3"
	youtubeUpload = "https://www.googleapis.com/upload/youtube/v3"
	youtubeGaming = "20" // video category id

	// History reads at most this many pages of 50 uploads (5 quota units).
	youtubeMaxPages = 5
)

// YouTube posts Shorts through the YouTube Data API v3. A vertical video
// up to 3 minutes long is a Short; there's no flag for it.
//
// Until the Google Cloud project passes YouTube's API compliance audit,
// every upload is locked to private, even when we ask for public.
type YouTube struct {
	oauth *oauth2.Config
	// Overridden in tests.
	apiBase, uploadBase string
}

func NewYouTube(c Credentials) *YouTube {
	return &YouTube{
		oauth: &oauth2.Config{
			ClientID:     c.ID,
			ClientSecret: c.Secret,
			Endpoint:     endpoints.Google,
			Scopes: []string{
				"https://www.googleapis.com/auth/youtube.upload",
				"https://www.googleapis.com/auth/youtube.readonly", // channel, uploads and stats
			},
		},
		apiBase:    youtubeAPI,
		uploadBase: youtubeUpload,
	}
}

func (y *YouTube) Name() string     { return "youtube" }
func (y *YouTube) Label() string    { return "YouTube Shorts" }
func (y *YouTube) Src() string      { return "yt_shorts" }
func (y *YouTube) Configured() bool { return y.oauth.ClientID != "" && y.oauth.ClientSecret != "" }

func (y *YouTube) AuthURL(state, redirectURL string) string {
	c := *y.oauth
	c.RedirectURL = redirectURL
	// offline + consent so Google returns a refresh token every time we connect.
	return c.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
}

func (y *YouTube) Exchange(ctx context.Context, code, redirectURL string) (*oauth2.Token, error) {
	c := *y.oauth
	c.RedirectURL = redirectURL
	tok, err := c.Exchange(ctx, code)
	if err == nil && tok.RefreshToken == "" {
		return nil, errors.New("google sent no refresh token; remove the app's access at myaccount.google.com/permissions and connect again")
	}
	return tok, err
}

func (y *YouTube) TokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	return y.oauth.TokenSource(ctx, tok)
}

// Google refresh tokens don't expire while the app is "In production" and
// in use, so the refresh token is all we keep. Access tokens come from it.
func (y *YouTube) TokenEnv() string                        { return "YOUTUBE_REFRESH_TOKEN" }
func (y *YouTube) SavedToken(tok *oauth2.Token) string     { return tok.RefreshToken }
func (y *YouTube) RestoreToken(saved string) *oauth2.Token { return &oauth2.Token{RefreshToken: saved} }

// Account returns the channel's @handle (or its title if it has none).
func (y *YouTube) Account(ctx context.Context, ts oauth2.TokenSource) (Account, error) {
	ch, err := y.channel(ctx, ts)
	if err != nil {
		return Account{}, err
	}
	handle := ch.Snippet.CustomURL
	if handle == "" {
		handle = ch.Snippet.Title
	}
	return Account{ID: ch.ID, Handle: handle}, nil
}

type ytChannel struct {
	ID      string `json:"id"`
	Snippet struct {
		Title     string `json:"title"`
		CustomURL string `json:"customUrl"`
	} `json:"snippet"`
	ContentDetails struct {
		RelatedPlaylists struct {
			Uploads string `json:"uploads"`
		} `json:"relatedPlaylists"`
	} `json:"contentDetails"`
}

func (y *YouTube) channel(ctx context.Context, ts oauth2.TokenSource) (ytChannel, error) {
	var body struct {
		Items []ytChannel `json:"items"`
	}
	if err := y.get(ctx, ts, "/channels?part=snippet,contentDetails&mine=true", &body); err != nil {
		return ytChannel{}, err
	}
	if len(body.Items) == 0 {
		return ytChannel{}, errors.New("this Google account has no YouTube channel")
	}
	return body.Items[0], nil
}

type ytVideo struct {
	Snippet struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		CategoryID  string `json:"categoryId"`
	} `json:"snippet"`
	Status struct {
		PrivacyStatus           string `json:"privacyStatus"`
		SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids"`
	} `json:"status"`
}

// Upload sends the video with a resumable upload, which streams the body:
//  1. POST the metadata; the Location header is the upload session URL.
//  2. PUT the bytes there; the response is the new video.
func (y *YouTube) Upload(ctx context.Context, ts oauth2.TokenSource, v Video) (Result, error) {
	var meta ytVideo
	meta.Snippet.Title = ytText(v.Title, 100)
	meta.Snippet.Description = ytText(v.Caption, 5000)
	meta.Snippet.CategoryID = youtubeGaming
	meta.Status.PrivacyStatus = "public"
	payload, err := json.Marshal(meta)
	if err != nil {
		return Result{}, err
	}

	client := oauth2.NewClient(ctx, ts)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		y.uploadBase+"/videos?uploadType=resumable&part=snippet,status", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(v.Size, 10))
	req.Header.Set("X-Upload-Content-Type", "video/*")
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("youtube: start upload: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return Result{}, ytError(resp, "start upload")
	}
	resp.Body.Close()
	session := resp.Header.Get("Location")
	if session == "" {
		return Result{}, errors.New("youtube: start upload: no session URL")
	}

	put, err := http.NewRequestWithContext(ctx, http.MethodPut, session, v.Body)
	if err != nil {
		return Result{}, err
	}
	put.ContentLength = v.Size // the body is a pipe, so net/http can't tell
	put.Header.Set("Content-Type", "video/*")
	resp, err = client.Do(put)
	if err != nil {
		return Result{}, fmt.Errorf("youtube: upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return Result{}, ytError(resp, "upload")
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil || created.ID == "" {
		return Result{}, fmt.Errorf("youtube: upload: unreadable response (%v)", err)
	}
	return Result{RemoteID: created.ID, URL: shortURL(created.ID), Status: StatusPublished}, nil
}

// History lists the channel's uploads since a time, newest first, with
// privacy and stats. It includes private videos (it's your own channel).
func (y *YouTube) History(ctx context.Context, ts oauth2.TokenSource, since time.Time) ([]Post, error) {
	ch, err := y.channel(ctx, ts)
	if err != nil {
		return nil, err
	}
	var posts []Post
	page := ""
	for range youtubeMaxPages {
		q := url.Values{
			"part":       {"snippet"},
			"playlistId": {ch.ContentDetails.RelatedPlaylists.Uploads},
			"maxResults": {"50"},
		}
		if page != "" {
			q.Set("pageToken", page)
		}
		var body struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Snippet struct {
					Title       string    `json:"title"`
					PublishedAt time.Time `json:"publishedAt"` // when it was uploaded
					ResourceID  struct {
						VideoID string `json:"videoId"`
					} `json:"resourceId"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if err := y.get(ctx, ts, "/playlistItems?"+q.Encode(), &body); err != nil {
			return nil, err
		}
		done := body.NextPageToken == ""
		for _, it := range body.Items {
			if it.Snippet.PublishedAt.Before(since) {
				done = true
				continue
			}
			id := it.Snippet.ResourceID.VideoID
			posts = append(posts, Post{ID: id, Title: it.Snippet.Title, URL: shortURL(id), PostedAt: it.Snippet.PublishedAt})
		}
		if done {
			break
		}
		page = body.NextPageToken
	}
	return posts, y.fillDetails(ctx, ts, posts)
}

// fillDetails adds privacy and stats, 50 videos per call.
func (y *YouTube) fillDetails(ctx context.Context, ts oauth2.TokenSource, posts []Post) error {
	for start := 0; start < len(posts); start += 50 {
		batch := posts[start:min(start+50, len(posts))]
		ids := make([]string, len(batch))
		for i, p := range batch {
			ids[i] = p.ID
		}
		var body struct {
			Items []struct {
				ID     string `json:"id"`
				Status struct {
					PrivacyStatus string `json:"privacyStatus"`
				} `json:"status"`
				Statistics struct {
					// The API sends counts as strings; likes and comments
					// are missing when the creator hides them.
					Views    string `json:"viewCount"`
					Likes    string `json:"likeCount"`
					Comments string `json:"commentCount"`
				} `json:"statistics"`
			} `json:"items"`
		}
		q := url.Values{"part": {"status,statistics"}, "id": {strings.Join(ids, ",")}}
		if err := y.get(ctx, ts, "/videos?"+q.Encode(), &body); err != nil {
			return err
		}
		byID := make(map[string]int, len(batch))
		for i, p := range batch {
			byID[p.ID] = i
		}
		for _, it := range body.Items {
			i, ok := byID[it.ID]
			if !ok {
				continue
			}
			batch[i].Privacy = it.Status.PrivacyStatus
			batch[i].Stats = Stats{
				Views:    atoi(it.Statistics.Views),
				Likes:    atoi(it.Statistics.Likes),
				Comments: atoi(it.Statistics.Comments),
			}
		}
	}
	return nil
}

func (y *YouTube) get(ctx context.Context, ts oauth2.TokenSource, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, y.apiBase+path, nil)
	if err != nil {
		return err
	}
	resp, err := oauth2.NewClient(ctx, ts).Do(req)
	if err != nil {
		return fmt.Errorf("youtube: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ytError(resp, "request")
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// ytError turns Google's error JSON into a message worth showing, e.g.
// "youtube: upload: quotaExceeded: The request cannot be completed because you have exceeded your quota."
func ytError(resp *http.Response, what string) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &body) != nil || body.Error.Message == "" {
		return fmt.Errorf("youtube: %s: %s", what, resp.Status)
	}
	if len(body.Error.Errors) > 0 && body.Error.Errors[0].Reason != "" {
		return fmt.Errorf("youtube: %s: %s: %s", what, body.Error.Errors[0].Reason, body.Error.Message)
	}
	return fmt.Errorf("youtube: %s: %s", what, body.Error.Message)
}

func shortURL(id string) string { return "https://youtube.com/shorts/" + id }

// ytText fits text to YouTube's rules: no angle brackets in titles or
// descriptions, and a length limit in characters.
func ytText(s string, limit int) string {
	s = strings.NewReplacer("<", "‹", ">", "›").Replace(strings.TrimSpace(s))
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
