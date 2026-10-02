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
| `/` | Solo game: pick a goal (3–30s), the clock hides, tap to stop. Shows best/average per goal for the current visit (resets on refresh) and has a "Challenge a friend" share. |
| `/party` | Goal Challenge pass-the-phone mode for 2–8 players, with a hidden-until-reveal leaderboard. |
| `/online` | Online multiplayer: create a room, share the code, link or QR, and 2–8 players play Goal Challenge on their own phones over 1–10 rounds. Lowest total error wins. |
| `/r/<code>` | Invite link for a room (not indexed). `/r/<code>/qr.svg` is its QR code. |
| `/api/rooms`, `/ws/<code>` | Room creation (POST) and the room WebSocket. |
| `/how-to-play` | Rules, modes and tips. This is SEO content, and AdSense needs substantive content like this. |
| `/privacy` | Privacy policy covering AdSense cookies (required by AdSense). |
| `/get?src=<placement>` | The **only** outbound link to the App Store. Logs `app_store_click` and adds `pt`/`ct` campaign params. |
| `/ads.txt`, `/robots.txt`, `/sitemap.xml`, `/healthz` | Operational endpoints. |

```
main.go                  server bootstrap + graceful shutdown
internal/config          env config + validation
internal/server          router, handlers, template rendering, online.go (room HTTP/WebSocket), tests
internal/rooms           Online multiplayer game logic: rooms, rounds, scoring, hub (in memory)
web/templates            layout, partials (ad, promo), pages
web/static               app.css, app.js (game engine + particles), app icons, og.png
```

## Funnel to the app

- **Smart App Banner**: `<meta name="apple-itunes-app">` makes iOS Safari show a native Get/Open banner.
- **Placements**: header, the promo card on Home and Party, a nudge after the 3rd solo round, How to play, the footer, and the share panel (`share_solo` / `share_party`). Each has its own `src`.
- **Sharing**: after a solo round (stealth on) or a party reveal, players get a 1080×1080 result card drawn in the browser, with the App Store badge on it. They can share it natively or post to X, Facebook or WhatsApp, copy the text, or save the image. Every shared message includes the site link and a direct App Store link (with the `ct=web-share_<mode>` campaign tag), since it's opened on other people's phones. Links clicked on this site still go through `/get`.
- **Web-exclusive gap**: Tournament, Teams and extra themes are app-only (iPhone and iPad), and the site says so wherever it's relevant.
- **Attribution**: set `APP_STORE_PROVIDER_TOKEN` to see installs per `ct=web-<src>` campaign in App Store Connect > App Analytics. Server logs record every click, and GA4 (optional) gets an `app_store_click` event.

## Ads (Google AdSense)

1. Get approved, then set `ADSENSE_CLIENT=ca-pub-…` and create three display ad units for `ADSENSE_SLOT_SIDEBAR` (300×600, desktop only), `ADSENSE_SLOT_INCONTENT` and `ADSENSE_SLOT_FOOTER`.
2. `/ads.txt` is generated automatically from `ADSENSE_CLIENT`.
3. In development, dashed placeholders show where the ads will go. In production, unconfigured slots render nothing.
4. **Placements**: on the home page, phones get an in-content unit after the app promo card and a `footer` unit at the bottom. Desktop gets the in-content unit plus the 300×600 sidebar, and the bottom unit is hidden there. Party has one in-content unit, and How to play has in-content plus footer.
5. **Placement policy**: ads are intentionally kept away from the tap zone. AdSense bans placements that invite accidental clicks, and a game where people tap rapidly is a high-risk case. Don't put units next to the timer or the result buttons.
6. **Auto ads**: if you turn on AdSense Auto ads, disable **anchor** and **vignette** formats for this site (AdSense > Ads > By site > Edit). A bottom-anchored banner sits right under a phone player's thumb while they tap the game, which leads to accidental clicks and policy strikes.
7. **EU/UK/CH visitors**: Google requires a certified consent platform. The easiest option is AdSense's built-in *Privacy & messaging* → GDPR message, which needs no code changes.
8. Units are only initialised when visible, so the desktop sidebar never requests an ad on phones.

## Share image

`web/static/img/og.png` (1200×630) is used for `og:image` and `twitter:image` on every page. Its source is `tools/og/og.html`. To change it, edit that file and screenshot it at a 1200×630 viewport, e.g.:

```sh
npx playwright screenshot --viewport-size=1200,630 --wait-for-timeout=500 tools/og/og.html web/static/img/og.png
```

The URL is content-hashed, but social platforms cache previews aggressively, so use each platform's debugger (e.g. Facebook Sharing Debugger) to refresh after a change.

## Online multiplayer

Rooms live in the server's memory (`internal/rooms`); browsers talk to them over a WebSocket (`internal/server/online.go`). Each phone times its own taps, so network lag never affects a score; the server only opens rounds, collects results and reveals them together.

- **Hosting:** run **one** machine (`fly scale count 1`), or players can land on a machine that doesn't have their room. Deploys end open rooms.
- **Capacity:** every player holds one connection. Without an `[http_service.concurrency]` block Fly caps a machine at about 25 connections; with `type = "connections"`, `soft_limit = 800`, `hard_limit = 1000`, one 256 MB machine handles about 1,000 (estimate). Details and how to grow: `flyio-setup.md` → Scaling.
- **Limits built in:** 8 players per room, 1,000 open rooms, rooms close after 30 idle minutes, per-IP limits on creating rooms and connecting.

## Before launch

- App Store links use Apple's official black "Download on the App Store" badge (`web/static/img/app-store-badge.svg`). Per [Apple's guidelines](https://developer.apple.com/app-store/marketing/guidelines/), don't alter it or show it smaller than 40px tall.
- Put the service behind TLS (Cloud Run, Fly.io, Render and similar all handle this). HSTS is sent when `APP_ENV=production`.
- Review `/privacy` against your actual analytics and ad setup.

### SEO checklist (once the domain is live)

1. Set `APP_ENV=production` and `BASE_URL=https://<your-domain>`. Startup fails if it's still localhost or http. Canonical tags, `robots.txt`, `sitemap.xml` and share images all follow it, and any other host (`www.`, the platform's default domain) is 301-redirected to it.
2. Turn on the host's "HTTPS only" / HTTP→HTTPS redirect.
3. Verify the site in [Google Search Console](https://search.google.com/search-console) and [Bing Webmaster Tools](https://www.bing.com/webmasters). Either use DNS verification, or set `GOOGLE_SITE_VERIFICATION` / `BING_SITE_VERIFICATION`.
4. Submit `https://<your-domain>/sitemap.xml` in both, then use URL Inspection to request indexing of `/`.
5. Check the structured data with Google's [Rich Results Test](https://search.google.com/test/rich-results) and share previews with each platform's debugger.
