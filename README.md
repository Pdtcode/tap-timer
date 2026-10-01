# Tap Timer — web

A browser version of [Tap Timer: Beat the Clock](https://apps.apple.com/us/app/tap-timer-beat-the-clock/id6802904982), built with Go + Gin. It's ad-supported and funnels players to the iOS app.

## Run

```sh
go run .                 # http://localhost:8080
go test ./...
docker build -t taptimer . && docker run -p 8080:8080 -e BASE_URL=https://your.domain taptimer
```

All configuration is via environment variables. See [`.env.example`](.env.example). Templates and static files are embedded with `go:embed`, so the binary is fully self-contained.

## What's here

| Path | What it is |
|---|---|
| `/` | Solo game: pick a goal (3–30s), the clock hides, tap to stop. Tracks best/average per goal locally and has a "Challenge a friend" share. |
| `/party` | Goal Challenge pass-the-phone mode for 2–8 players, with a hidden-until-reveal leaderboard. |
| `/how-to-play` | Rules, modes and tips. This is SEO content, and AdSense needs substantive content like this. |
| `/privacy` | Privacy policy covering AdSense cookies (required by AdSense). |
| `/get?src=<placement>` | The **only** outbound link to the App Store. Logs `app_store_click` and adds `pt`/`ct` campaign params. |
| `/ads.txt`, `/robots.txt`, `/sitemap.xml`, `/healthz` | Operational endpoints. |

```
main.go                  server bootstrap + graceful shutdown
internal/config          env config + validation
internal/server          router, handlers, template rendering, tests
web/templates            layout, partials (ad, promo), pages
web/static               app.css, app.js (game engine + particles), app icons, og.png
```

## Funnel to the app

- **Smart App Banner**: `<meta name="apple-itunes-app">` makes iOS Safari show a native Get/Open banner.
- **Placements**: header, the promo card on Home and Party, a nudge after the 3rd solo round, the "More themes" link, How to play and the footer. Each has its own `src`.
- **Web-exclusive gap**: Tournament, Teams, extra themes and Apple Watch/TV support are app-only, and the site says so wherever it's relevant.
- **Attribution**: set `APP_STORE_PROVIDER_TOKEN` to see installs per `ct=web-<src>` campaign in App Store Connect > App Analytics. Server logs record every click, and GA4 (optional) gets an `app_store_click` event.

## Ads (Google AdSense)

1. Get approved, then set `ADSENSE_CLIENT=ca-pub-…` and create three display ad units for `ADSENSE_SLOT_SIDEBAR` (300×600, desktop only), `ADSENSE_SLOT_INCONTENT` and `ADSENSE_SLOT_FOOTER`.
2. `/ads.txt` is generated automatically from `ADSENSE_CLIENT`.
3. In development, dashed placeholders show where the ads will go. In production, unconfigured slots render nothing.
4. **Placement policy**: ads are intentionally kept away from the tap zone. AdSense bans placements that invite accidental clicks, and a game where people tap rapidly is a high-risk case. Don't put units next to the timer or the result buttons.
5. **EU/UK/CH visitors**: Google requires a certified consent platform. The easiest option is AdSense's built-in *Privacy & messaging* → GDPR message, which needs no code changes.
6. Units are only initialised when visible, so the desktop sidebar never requests an ad on phones.

## Share image

`web/static/img/og.png` (1200×630) is used for `og:image` and `twitter:image` on every page. Its source is `tools/og/og.html`. To change it, edit that file and screenshot it at a 1200×630 viewport, e.g.:

```sh
npx playwright screenshot --viewport-size=1200,630 --wait-for-timeout=500 tools/og/og.html web/static/img/og.png
```

The URL is content-hashed, but social platforms cache previews aggressively, so use each platform's debugger (e.g. Facebook Sharing Debugger) to refresh after a change.

## Before launch

- Replace the "Get it on the App Store" button with Apple's official badge from [Apple's marketing tools](https://tools.applemarketingtools.com) if you want the badge look.
- Put the service behind TLS (Cloud Run, Fly.io, Render and similar all handle this). HSTS is sent when `APP_ENV=production`.
- Review `/privacy` against your actual analytics and ad setup.
