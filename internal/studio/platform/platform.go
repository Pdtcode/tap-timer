// Package platform talks to YouTube, Instagram and TikTok: OAuth, uploads,
// and reading back what you've posted. Videos are streamed straight through;
// nothing here writes them to disk.
package platform

import (
	"context"
	"errors"
	"io"
	"time"

	"golang.org/x/oauth2"
)

// ErrNotImplemented marks the parts of a client that are still scaffolding.
var ErrNotImplemented = errors.New("not implemented yet")

// Video is one upload. Body is read exactly once, front to back, and is
// exactly Size bytes (every platform wants the length before the bytes).
// Every upload is published right away.
type Video struct {
	Body    io.Reader
	Size    int64
	Title   string // YouTube only; the others use Caption
	Caption string // the rendered caption template for this platform
}

// Status is where an upload stands on one platform.
type Status string

const (
	StatusPublished Status = "published"
	StatusDraft     Status = "draft" // in the TikTok inbox: finish it in the app
	StatusFailed    Status = "failed"
)

// Result is what an upload produced.
type Result struct {
	RemoteID string
	URL      string
	Status   Status
}

// Stats are a video's numbers.
type Stats struct {
	Views, Likes, Comments int64
}

// Post is one video already on a platform, for the dashboard and the streak.
type Post struct {
	ID       string
	Title    string
	URL      string
	PostedAt time.Time
	Privacy  string // as the platform says it: "public", "private", ...
	Stats    Stats
}

// Account is who a token belongs to, shown on the Accounts page.
type Account struct {
	ID     string
	Handle string
}

type Platform interface {
	Name() string  // stable key: "youtube", "instagram", "tiktok"
	Label() string // for people: "YouTube Shorts"
	// Configured reports whether the developer app's credentials are set.
	Configured() bool
	// Src is the /get?src= value for links from this platform (e.g. the bio link).
	Src() string

	AuthURL(state, redirectURL string) string
	Exchange(ctx context.Context, code, redirectURL string) (*oauth2.Token, error)
	TokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource

	// There's no database, so a connection survives restarts only as a secret
	// you set once: TokenEnv names it, SavedToken is its value, and
	// RestoreToken turns it back into a token at startup.
	TokenEnv() string
	SavedToken(tok *oauth2.Token) string
	RestoreToken(saved string) *oauth2.Token

	Account(ctx context.Context, ts oauth2.TokenSource) (Account, error)
	Upload(ctx context.Context, ts oauth2.TokenSource, v Video) (Result, error)
	// History lists your posts since a time, newest first, with their stats.
	History(ctx context.Context, ts oauth2.TokenSource, since time.Time) ([]Post, error)
}

// Credentials are a developer app's id and secret.
type Credentials struct {
	ID, Secret string
}

// All returns the three platforms in display order.
func All(yt, ig, tt Credentials) []Platform {
	return []Platform{NewYouTube(yt), NewInstagram(ig), NewTikTok(tt)}
}
