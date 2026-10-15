package server

// In-house ads: our own projects, filling ad slots AdSense doesn't.
//
// Plain config rather than a database on purpose: the list is short, changes
// rarely, and belongs in version control next to the markup that renders it.
// Ported from URStoryBook's src/lib/house-ads.ts; keep the two in step.
//
// Every campaign is a finished HTML creative shown in an iframe. The creative
// owns its own markup, animation and click-through, so the "ad" partial never
// wraps it in a link or gives it a background or border.
//
// When a house ad shows:
//   - the slot has no AdSense unit configured (dev, or before approval);
//   - AdSense reports the slot unfilled (app.js swaps the house ad in);
//   - or the slot is one of the HouseAdShare reserved for our own campaigns.

import (
	"fmt"
	"html/template"
	"math/rand/v2"
	"strings"
)

// HouseAd is one campaign. Creatives is keyed by slot name ("incontent",
// "footer", "sidebar"); a campaign only competes for slots it lists.
type HouseAd struct {
	ID    string
	Title string // the iframe's accessible title
	// Each slot holds a LIST in order of preference, widest first: app.js
	// shows the first one that suits the viewport and fits the slot.
	Creatives map[string][]HouseCreative
}

type HouseCreative struct {
	// A path under web/static (starting "/static/") or an absolute URL on the
	// advertiser's own domain. Local creatives get the asset version appended so
	// the immutable cache never serves a stale one.
	Src string

	// Fixed units: their one true pixel size, set as the iframe's width/height
	// so the box is reserved before load (no layout shift).
	Width, Height int

	// Responsive units: fill the slot at this CSS aspect ratio instead, e.g.
	// "16 / 5". The ratio must match what the creative expects or it crops.
	// MaxWidth optionally caps it so a wide slot doesn't stretch it absurdly.
	AspectRatio string
	MaxWidth    int

	// "mobile" or "desktop" restricts the creative to one class of viewport
	// (split at 560px, the site's phone breakpoint). Empty means any.
	Viewport string
}

func (c HouseCreative) responsive() bool { return c.AspectRatio != "" }

// HouseAdShare is the fraction of slots, 0…1, that show a house ad even when
// AdSense is configured. Every slot taken this way is revenue forgone, so it
// defaults to 0: house ads fill only unsold or unconfigured inventory. Raise it
// while promoting a launch matters more than the ad revenue. A var, not a
// const: 1/HouseAdShare below would be a compile-time division by zero.
var HouseAdShare = 0.0

var houseAds = []HouseAd{
	{
		// Grail Seekers. The creative is a self-contained file copied from
		// URStoryBook (web/static/gs-banner.html; utm_source retagged to taptimer)
		// with logo and photos inline: ZERO external requests. It runs its own
		// crossfade, pausing off-screen and under prefers-reduced-motion.
		//
		// Deliberately not under /ads/ nor named *-ad-*: EasyList and uBlock
		// filter those patterns and a real share of visitors would see nothing.
		// The site is dark-only, so it always gets the dark variant.
		ID:    "grail-seekers",
		Title: "Grail Seekers",
		Creatives: map[string][]HouseCreative{
			// The leaderboard only fits the home page's 760px column; the
			// 688px columns elsewhere and phones get the box.
			"incontent": {
				{Src: "/static/gs-banner.html?size=728x90&theme=dark", Width: 728, Height: 90, Viewport: "desktop"},
				{Src: "/static/gs-banner.html?size=300x250&theme=dark", Width: 300, Height: 250},
				// Last resort for a phone narrower than the box.
				{Src: "/static/gs-banner.html?size=320x50&theme=dark", Width: 320, Height: 50, Viewport: "mobile"},
			},
			"footer": {
				{Src: "/static/gs-banner.html?size=728x90&theme=dark", Width: 728, Height: 90, Viewport: "desktop"},
				{Src: "/static/gs-banner.html?size=300x250&theme=dark", Width: 300, Height: 250},
			},
			// The 300px desktop column can afford the half page.
			"sidebar": {
				{Src: "/static/gs-banner.html?size=300x600&theme=dark", Width: 300, Height: 600},
				{Src: "/static/gs-banner.html?size=300x250&theme=dark", Width: 300, Height: 250},
			},
		},
	},
	{
		// Han Jan Pocha: Korean kitchen & pocha in OKC.
		//
		// HOSTED REMOTELY and deliberately not copied in, so updates at the
		// source appear here with no redeploy. The trade-off is a third-party
		// connection, and an outage there leaves an empty box. Its path is under
		// /ads/, so ad blockers will hide it; that's the advertiser's call.
		// It paints its own background and supplies its own link and target.
		ID:    "han-jan-pocha",
		Title: "Han Jan Pocha: Korean Kitchen & Pocha in OKC",
		Creatives: map[string][]HouseCreative{
			// `strip` is the creative's own pick for a content-width slot,
			// capped so a 688px column doesn't stretch it into a 215px-tall
			// slab. The leaderboard takes the home page's wider column.
			"incontent": {
				{Src: hanJanSrc("leaderboard"), Width: 728, Height: 90, Viewport: "desktop"},
				{Src: hanJanSrc("strip"), AspectRatio: "16 / 5", MaxWidth: 560},
			},
			"footer": {
				{Src: hanJanSrc("leaderboard"), Width: 728, Height: 90, Viewport: "desktop"},
				{Src: hanJanSrc("strip"), AspectRatio: "16 / 5", MaxWidth: 560},
			},
			// halfpage is the creative's 300x600 sidebar layout; the square
			// looked lost in the tall column.
			"sidebar": {
				{Src: hanJanSrc("halfpage"), Width: 300, Height: 600},
			},
		},
	},
}

func hanJanSrc(size string) string {
	return "https://hanjanpochaok.com/ads/banner.html?size=" + size + "&src=taptimer&to=/menu"
}

// HouseSlot is what the "ad" partial renders: every campaign that targets
// the slot, in config order. app.js picks one per slot as the page comes up.
//
// The pick has to happen in the browser. Pages hide slots per viewport (the
// sidebar on phones, the home footer beside the sidebar), and the server
// can't know which. Rotating over the rendered slots put the two VISIBLE
// slots on a wide screen two apart, so with two campaigns both always showed
// the same one. Counting only slots actually shown avoids that. None of the
// candidates download until one is inserted.
type HouseSlot struct {
	Campaigns []HouseCampaign
}

type HouseCampaign struct {
	ID     string
	Title  string
	Frames []HouseFrame // the campaign's creatives for this slot, best fit first
}

type HouseFrame struct {
	Src      string
	Width    int // 0 for responsive units
	Height   int
	Viewport string
	// Inline styles built here from trusted config: html/template would
	// otherwise reject values like "16 / 5" inside a style attribute.
	BoxStyle   template.CSS
	FrameStyle template.CSS
}

// AdRotation counts a page view's ad slots, to spread the HouseAdShare
// reserved slots evenly. It starts at a random point so a page with only a few
// slots doesn't reserve the same one on every visit.
type AdRotation struct{ n int }

func NewAdRotation() *AdRotation { return &AdRotation{n: rand.IntN(1 << 16)} }

func (r *AdRotation) next() int {
	if r == nil { // page data built without pageData(): no rotation, not a panic
		return 0
	}
	n := r.n
	r.n++
	return n
}

// resolveHouseSlot lists the campaigns, and their creatives, for one slot.
func resolveHouseSlot(slot, assetVersion string) *HouseSlot {
	hs := &HouseSlot{}
	for _, a := range houseAds {
		if len(a.Creatives[slot]) == 0 {
			continue
		}
		c := HouseCampaign{ID: a.ID, Title: a.Title}
		for _, cr := range a.Creatives[slot] {
			c.Frames = append(c.Frames, houseFrame(cr, assetVersion))
		}
		hs.Campaigns = append(hs.Campaigns, c)
	}
	if len(hs.Campaigns) == 0 {
		return nil
	}
	return hs
}

func houseFrame(c HouseCreative, assetVersion string) HouseFrame {
	src := c.Src
	if strings.HasPrefix(src, "/static/") {
		sep := "?"
		if strings.Contains(src, "?") {
			sep = "&"
		}
		src += sep + "v=" + assetVersion
	}
	f := HouseFrame{Src: src, Viewport: c.Viewport}
	if c.responsive() {
		box := "width:100%"
		if c.MaxWidth > 0 {
			box += fmt.Sprintf(";max-width:%dpx", c.MaxWidth)
		}
		f.BoxStyle = template.CSS(box)
		f.FrameStyle = template.CSS("width:100%;aspect-ratio:" + c.AspectRatio)
	} else {
		f.Width, f.Height = c.Width, c.Height
		f.BoxStyle = template.CSS(fmt.Sprintf("height:%dpx", c.Height))
	}
	return f
}

// preemptWithHouseAd reports whether this slot is one of the HouseAdShare
// reserved for our own campaigns despite AdSense being available. Spread
// evenly: every Nth slot, N being the reciprocal of the share.
func preemptWithHouseAd(n int) bool {
	if HouseAdShare <= 0 {
		return false
	}
	if HouseAdShare >= 1 {
		return true
	}
	everyN := max(2, int(1/HouseAdShare+0.5))
	return n%everyN == 0
}
