# Deploying Tap Timer on Fly.io

Setup guide for running the Go server on [Fly.io](https://fly.io) using the existing `Dockerfile`. Last updated 2026-10-02. Written from general knowledge of Fly.io, so check anything marked **(verify)** against `fly help` and Fly's docs, since commands and pricing change.

## Why Fly.io for this app

- **No code changes:** it runs the existing Docker image and the real Go server. The canonical-host redirect, App Store click logging (`/get`), HEAD support and security headers all work as written.
- **WebSockets:** long-lived connections are supported. Online multiplayer (`/online`) depends on them; see [Scaling](#9-scaling) for its one-machine requirement and capacity.
- **Scaling:** scale up a machine, scale out to more machines or regions, and later route each room to the machine that owns it with `fly-replay`.
- **Cost:** pay-as-you-go. A Go server this size fits the smallest machine (256MB). There's no free tier for new accounts, so expect a few dollars a month for one always-on machine. **(verify current pricing)**

## What the app already does that matters here

| App behaviour | Fly.io setting it needs |
|---|---|
| Listens on `PORT` (default `8080`); the Dockerfile sets `APP_ENV=production` | `internal_port = 8080` |
| `GET /healthz` returns `200 ok` (exempt from the host redirect) | HTTP health check on `/healthz` |
| In production, `BASE_URL` must be the public **https** origin, or startup fails | Set `BASE_URL=https://<app>.fly.dev` now; switch to your domain later |
| Redirects any other host (`www.`, `*.fly.dev` once you have a domain) to `BASE_URL` | Nothing; it works behind Fly's proxy because `Host` is passed through |
| Graceful shutdown on `SIGTERM` (10s) | Fly sends `SIGINT`/`SIGTERM` on deploys and stops. The default `kill_timeout` is fine |
| Logs are JSON on stdout (`slog`) | `fly logs` shows them |

---

## 1. Install the CLI and sign in

```powershell
# Windows (built-in PowerShell; pwsh not required)
iwr https://fly.io/install.ps1 -useb | iex
```
```sh
# macOS / Linux
curl -L https://fly.io/install.sh | sh
```
```sh
fly auth signup   # or: fly auth login
```

A credit card is required to create apps. **(verify)**

## 2. Tidy the Docker build context (optional)

`.dockerignore` already skips `.git`, `.env` and binaries. Also add the local research folder so it isn't uploaded with every deploy:

```
doc
```

## 3. Create the app

From the repo root:

```sh
fly launch --no-deploy
```

- It detects the `Dockerfile`. Accept it.
- Pick an app name. It becomes `https://<name>.fly.dev`, so e.g. `tap-timer` (this app: `https://tap-timer.fly.dev`).
- Pick a primary region close to most players, e.g. `dfw` (Dallas, US Central; this app uses it), `iad` (US East) or `lhr` (London). Latency barely matters for this game since timing happens on the device.
- Say **no** to Postgres, Redis, Tigris and other add-ons. The app needs none.

It writes a `fly.toml`. Replace it with this (keep your app name and region):

```toml
app = "tap-timer"
primary_region = "dfw"

[build]
  # uses ./Dockerfile

[env]
  APP_ENV = "production"
  PORT = "8080"
  BASE_URL = "https://tap-timer.com"   # was https://tap-timer.fly.dev until the domain was set up
  APP_STORE_ID = "6802904982"
  ADSENSE_CLIENT = "ca-pub-6274927128191860"   # public publisher ID; adds the AdSense tags and /ads.txt
  # Optional public settings (can also go in secrets):
  # GA_MEASUREMENT_ID = "G-XXXXXXX"
  # CONTACT_EMAIL = "you@example.com"

[http_service]
  internal_port = 8080
  force_https = true              # http:// -> https://
  auto_stop_machines = "stop"     # stop idle machines...
  auto_start_machines = true      # ...and start them on the next request
  min_machines_running = 0        # 0: cheapest, ~1s wake-up for the first visitor; 1: always warm
  # Without this block Fly caps each machine at about 20-25 connections (verify).
  # Every Online multiplayer player holds a WebSocket open, so raise it.
  [http_service.concurrency]
    type = "connections"
    soft_limit = 800
    hard_limit = 1000

  [[http_service.checks]]
    method = "GET"
    path = "/healthz"
    interval = "15s"
    timeout = "2s"
    grace_period = "5s"

[[vm]]
  size = "shared-cpu-1x"
  memory = "256mb"
```

**`[http_service.concurrency]`:** count `connections`, not `requests`: a WebSocket is one connection for as long as a player is in a room. In the repo's `fly.toml` since 2026-10-02.

**`min_machines_running`:**
- `1` keeps the site instantly responsive. Recommended, since first impressions drive app downloads.
- `0` lets the last machine stop when idle (cheapest). The next visitor waits while it boots, usually under a second or two for a small Go binary.

## 4. Secrets and settings

Values that are private, or that differ per environment, go in secrets. They're encrypted and exposed to the app as environment variables:

```sh
fly secrets set APP_STORE_PROVIDER_TOKEN=123456
fly secrets set ADSENSE_CLIENT=ca-pub-1234567890123456 \
  ADSENSE_SLOT_SIDEBAR=111 ADSENSE_SLOT_INCONTENT=222 ADSENSE_SLOT_FOOTER=333
fly secrets set GOOGLE_SITE_VERIFICATION=abc BING_SITE_VERIFICATION=def
fly secrets list
```

Setting secrets triggers a redeploy once the app exists. Never commit `.env`; it's already in `.gitignore` and `.dockerignore`.

## 5. Deploy

```sh
fly deploy
```

Fly builds the image on its remote builder (no local Docker needed) and starts the machine. Then check it:

```sh
fly status
fly logs                                         # JSON logs, incl. app_store_click lines
curl -I https://tap-timer.fly.dev/               # 200, security headers, HSTS
curl -I https://tap-timer.fly.dev/healthz        # 200
curl -s https://tap-timer.fly.dev/robots.txt     # Sitemap: https://tap-timer.fly.dev/sitemap.xml
curl -sI "https://tap-timer.fly.dev/get?src=header" | grep -i location   # App Store URL
fly open
```

**If it crash-loops:** run `fly logs`. The usual cause is `invalid config`, meaning `BASE_URL` isn't an https URL while `APP_ENV=production`.

## 6. Custom domain (when you have it)

1. Add certificates for the apex and `www`:
   ```sh
   fly certs add tap-timer.com
   fly certs add www.tap-timer.com
   ```
2. Get the app's IPs:
   ```sh
   fly ips list
   ```
   New apps usually have a shared IPv4 and a dedicated IPv6. That's enough for an HTTPS website. A dedicated IPv4 costs extra and is only needed for non-HTTP services. **(verify)**
3. At your DNS provider:
   - `tap-timer.com`: an `A` record to the IPv4 and an `AAAA` record to the IPv6,
   - `www.tap-timer.com`: a `CNAME` to `tap-timer.fly.dev`.

   Follow whatever `fly certs show tap-timer.com` asks for. It may also want an `_acme-challenge` CNAME for validation.
4. Wait for `fly certs check tap-timer.com` to show the certificate as issued.
5. Point the app at the domain:
   ```toml
   # fly.toml
   BASE_URL = "https://tap-timer.com"
   ```
   ```sh
   fly deploy
   ```
   The app now 301-redirects `www.tap-timer.com` and `tap-timer.fly.dev` to `https://tap-timer.com`. That's the canonical-host middleware, so only one copy of the site gets indexed.
6. Do the README's **SEO checklist**: Search Console and Bing verification, submit the sitemap, Rich Results Test.

**Cloudflare in front (optional):** if you proxy the domain through Cloudflare (orange cloud) for caching and DDoS protection, set SSL/TLS to **Full (strict)**. Fly certificate validation then needs the `_acme-challenge` DNS record, because Fly can't reach the domain directly through the proxy. **(verify)**

## 7. Continuous deploys from GitHub (optional)

1. Create a deploy token and add it to the GitHub repo as the secret `FLY_API_TOKEN`:
   ```sh
   fly tokens create deploy -x 999999h
   ```
2. Add `.github/workflows/deploy.yml`:
   ```yaml
   name: Deploy
   on:
     push:
       branches: [main]
   jobs:
     test-and-deploy:
       runs-on: ubuntu-latest
       concurrency: deploy
       steps:
         - uses: actions/checkout@v4
         - uses: actions/setup-go@v5
           with:
             go-version-file: go.mod
         - run: go vet ./... && go test ./...
         - uses: superfly/flyctl-actions/setup-flyctl@master
         - run: flyctl deploy --remote-only
           env:
             FLY_API_TOKEN: ${{ secrets.FLY_API_TOKEN }}
   ```

Every push to `main` then runs the tests and deploys only if they pass.

## 8. Day-to-day operations

| Task | Command |
|---|---|
| Live logs | `fly logs` |
| Machine status / health checks | `fly status`, `fly checks list` |
| List releases | `fly releases` |
| Roll back | `fly deploy --image <previous image from fly releases --image>` **(verify flag)** |
| SSH into the machine | `fly ssh console` (the image is distroless, so there's no shell; use logs instead) |
| Restart | `fly apps restart tap-timer` |
| Usage and billing | Fly dashboard → Billing |

## 9. Scaling

**More capacity in one region:**
```sh
fly scale memory 512           # bigger machine
fly scale count 2              # two machines; Fly's proxy load-balances
```

**Closer to players in other regions:**
```sh
fly scale count 3 --region dfw,lhr,syd
```

Because pages are identical for everyone and the game runs in the browser, the website itself needs very little. Putting Cloudflare in front offloads most static requests. **But don't add machines while Online multiplayer is on one machine** (below).

**Online multiplayer: one machine, and its capacity**

Online rooms (`/online`) keep each room in the memory of the machine that created it.

- **Run exactly one machine:** `fly scale count 1 -a tap-timer`. With two, Fly's proxy can send a friend's phone to the machine that doesn't have the room, and they get "That room doesn't exist". It fails only sometimes, which makes it look like a random bug.
- **Deploys and Fly maintenance end live rooms.** Players see "The server restarted for an update" and create a new room. Deploy when traffic is low.
- **Idle machines are fine:** `auto_stop_machines` doesn't stop a machine while it has open WebSocket connections. **(verify)**

Every player in a room holds one WebSocket connection open the whole time, so the limit that matters is **simultaneous connections on that one machine**:

| Limit | Value | Notes |
|---|---|---|
| Fly's default per-machine cap, with no `[http_service.concurrency]` block | about 20 soft / 25 hard **(verify)** | Roughly 20–25 people playing online at once (three full rooms), with page visitors sharing the same cap. Past the soft limit Fly tries to start another machine (there isn't one); past the hard limit new connections queue or fail. |
| With the concurrency block in the `fly.toml` above | 800 soft / 1,000 hard | Players plus visitors. |
| Memory (256 MB) | roughly 2,000–3,000 idle sockets | Estimate, not load-tested: about 20–50 KB per socket (two goroutines plus buffers). |
| CPU | not a constraint | A full 8-player round is a few dozen small JSON messages; pings every 25s. |
| Built into the app | **900 players online** (`ONLINE_MAX_PLAYERS`), 1,000 open rooms, 8 per room | Below Fly's hard limit on purpose: pages still load, and anyone past 900 gets a "All spots are full" waiting screen that joins automatically when a spot opens (returning players always get their seat back). Raise `ONLINE_MAX_PLAYERS` and `hard_limit` together. Plus per-IP limits: 10 room creations and 30 connections a minute. |

**Growing past that:**
1. Raise the concurrency limits and memory together: `fly scale memory 512`, then roughly double `soft_limit` / `hard_limit`.
2. More than one machine: route each room to the machine that owns it.
   - The machine that receives a request for a room it doesn't own replies with a `fly-replay: instance=<machine-id>` header, and Fly's proxy re-sends the request to that machine. **(verify header syntax)**
   - Encode the owning machine in the room code, or keep a tiny lookup table. Each machine knows its own ID from `FLY_MACHINE_ID`.
   - Set `min_machines_running` to the number of room machines.
3. Rooms spanning machines: add Redis (Upstash via `fly redis create`) for pub/sub.
4. Before any of that, measure: a small script that opens a few hundred fake players against `/online` shows the real numbers.

## Checklist

- [ ] `fly auth login`, `fly launch --no-deploy`
- [ ] Replace `fly.toml` (port 8080, `/healthz` check, `BASE_URL=https://<app>.fly.dev`, `force_https`)
- [ ] Add `doc` to `.dockerignore`
- [ ] `fly secrets set` for AdSense, provider token and verification tokens as needed
- [ ] `fly scale count 1` (Online multiplayer needs one machine) and add the `[http_service.concurrency]` block
- [ ] `fly deploy`, then check `/`, `/healthz`, `/robots.txt`, `/get?src=header`, `/online`, logs
- [ ] Optional: GitHub Actions deploy with `FLY_API_TOKEN`
- [ ] Later: `fly certs add` for the apex and `www`, DNS records, `BASE_URL` → domain, redeploy, SEO checklist
