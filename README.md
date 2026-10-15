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
| `/privacy` | The website's own privacy policy (the iOS app has a separate one): browser storage, online rooms, server logs, AdSense with the EEA/UK/CH consent message. The Analytics section appears only when `GA_MEASUREMENT_ID` is set. Keep it in sync with what the code stores and logs. |
| `/terms` | The website's terms of service: free play, conduct in online rooms, the app (Apple's standard licence), ads and links, no warranty. Linked in the footer and in the sitemap; also the terms URL for the Google OAuth consent screen. |
| `/get?src=<placement>` | The **only** outbound link to the App Store. Logs `app_store_click` and adds `pt`/`ct` campaign params. |
| `/studio` | Private dashboard (password, not indexed) for posting one vertical video to YouTube Shorts, Instagram Reels and TikTok at once, with caption templates, a daily streak and per-post views. No database. Off unless `STUDIO_PASSWORD` is set. YouTube works; Instagram and TikTok are stubs. See `doc/studio.md`. |
| `/ads.txt`, `/robots.txt`, `/sitemap.xml`, `/healthz` | Operational endpoints. |

```
main.go                  server bootstrap + graceful shutdown
internal/config          env config + validation
internal/server          router, handlers, template rendering, online.go (room HTTP/WebSocket), tests
internal/rooms           Online multiplayer game logic: rooms, rounds, scoring, hub (in memory)
web/templates            layout, partials (ad, promo), pages
web/static               app.css, app.js (game engine + particles), app icons, og.png
```

Responses are gzip-compressed (`gin-contrib/gzip`), except WebSockets and already-compressed images and fonts: a first visit is about 38 KB instead of 131 KB.

### Behaviour worth knowing

- **Particles:** one field covers the whole viewport and is drawn again inside each opaque panel (tap zones), so motes and tap sparks cross panel edges seamlessly. The page layer is sized to the largest viewport (`100lvh`) so iPhone toolbars hiding on scroll never leave a bare strip; `html` has the site background so overscroll isn't black. Tune with `MOTE_SPEED`, `density`/`max` and the `sway` values in `app.js`.
- **First tap:** Solo's tap zone shows "Loading…" (taps ignored, capped at 1.5s) until the pixel font is in and the page has settled. The share card's font and images load with the page, and the card is drawn once the browser is idle after a stop, so the first result never stutters.
- **Share card:** tapping the preview enlarges it in a `<dialog>` (tap anywhere or Esc to close); **Save image** downloads it.
- **Stats:** Best / Average / Rounds are per page view (reset on refresh), include practice rounds, and have a reset button. Stealth starts on every visit.

## Languages

The site is in English (at the root), Spanish (`/es/`), Brazilian Portuguese (`/pt/`), German (`/de/`) and Indonesian (`/id/`). Each language has its own URLs so each version can rank on its own; every page lists the others with `hreflang` tags, the sitemap does the same, and the footer has a language picker. There's no automatic redirect by browser language (search engines and travellers both dislike that).

- **Strings** are in `web/i18n/<code>.json` (flat keys, `{name}` placeholders). Templates use `{{t "key"}}`, `{{th "key"}}` for strings with markup, and `{{link "/party"}}` for links in the page's language. Keys starting with `js.` also go to `app.js`, which reads them from `#tt-i18n` with `tr()`; shared links, invite links and QR codes keep the language.
- **Prose pages** (how to play, privacy, terms) are translated as whole templates in `web/templates/pages/i18n/<code>/`. The translated privacy and terms say the English version applies if they differ.
- **The app is English-only**, so translated pages say so next to the app links (`layout.app_english`).
- **Tests** (`internal/i18n`) fail if a language is missing a string, or a translation changes a `{placeholder}` or an HTML tag.
- **Adding a language:** add it to `Langs` in `internal/i18n/i18n.go`, then add `web/i18n/<code>.json` and the three prose templates. The translations were written by Claude and haven't been reviewed by native speakers yet.

## Funnel to the app

- **Smart App Banner**: `<meta name="apple-itunes-app">` makes iOS Safari show a native Get/Open banner.
- **Placements**: header, the promo card on Home and Party, a nudge after the 3rd solo round, How to play, the footer, and the share panel (`share_solo` / `share_party`). Each has its own `src`.
- **Sharing**: after a solo round (stealth on) or a party reveal, players get a 1080×1080 result card drawn in the browser, with the App Store badge on it. They can share it natively or post to X, Facebook or WhatsApp, copy the text, or save the image. Shared messages contain no emojis. Every shared message includes the site link and a direct App Store link (with the `ct=web-share_<mode>` campaign tag), since it's opened on other people's phones. Links clicked on this site still go through `/get`.
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
- **Limits built in:** 900 players online at once (`ONLINE_MAX_PLAYERS`; kept below Fly's `hard_limit` so pages still load), 8 players per room, 1,000 open rooms, rooms close after 30 idle minutes, per-IP limits on creating rooms and connecting.
- **When full:** new players get an "All spots are full" waiting screen that retries every 8–14s and joins automatically when a spot opens. Players returning to a seat they hold always get back in.
- **Slow starts:** if creating or joining takes more than 300 ms (the machine waking up, or a slow network), the page shows "Connecting…" until the room is ready. While `/online` is open, the page pings `/healthz` every 2 minutes so the machine doesn't go to sleep before someone taps Create.
- **Cold starts:** with `min_machines_running = 0`, the first page load after the machine has slept waits ~1.5s while it boots (measured on Fly), and no page of ours can show during that time. `min_machines_running = 1` removes it for roughly $2/month (check Fly's pricing).
- **Testing locally:** `ONLINE_MAX_PLAYERS=1 go run .` lets you see the waiting screen with two browsers.

## Before launch

- App Store links use Apple's official black "Download on the App Store" badge (`web/static/img/app-store-badge.svg`). Per [Apple's guidelines](https://developer.apple.com/app-store/marketing/guidelines/), don't alter it or show it smaller than 40px tall.
- Put the service behind TLS (Cloud Run, Fly.io, Render and similar all handle this). HSTS is sent when `APP_ENV=production`.
- Review `/privacy` against your actual analytics and ad setup, and set `CONTACT_EMAIL` so it has a real contact (otherwise it points to the App Store listing). It was written to match the code, not reviewed by a lawyer.

### SEO checklist (once the domain is live)

1. Set `APP_ENV=production` and `BASE_URL=https://<your-domain>`. Startup fails if it's still localhost or http. Canonical tags, `robots.txt`, `sitemap.xml` and share images all follow it, and any other host (`www.`, the platform's default domain) is 301-redirected to it.
2. Turn on the host's "HTTPS only" / HTTP→HTTPS redirect.
3. Verify the site in [Google Search Console](https://search.google.com/search-console) and [Bing Webmaster Tools](https://www.bing.com/webmasters). Either use DNS verification, or set `GOOGLE_SITE_VERIFICATION` / `BING_SITE_VERIFICATION`.
4. Submit `https://<your-domain>/sitemap.xml` in both, then use URL Inspection to request indexing of `/`.
5. Check the structured data with Google's [Rich Results Test](https://search.google.com/test/rich-results) and share previews with each platform's debugger.
