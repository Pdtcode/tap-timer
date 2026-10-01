# Deploying Tap Timer on Fly.io

Setup guide for running the Go server on [Fly.io](https://fly.io) using the existing `Dockerfile`. Last updated 2026-10-01. Written from general knowledge of Fly.io, so check anything marked **(verify)** against `fly help` and Fly's docs, since commands and pricing change.

## Why Fly.io for this app

- **No code changes:** it runs the existing Docker image and the real Go server. The canonical-host redirect, App Store click logging (`/get`), HEAD support and security headers all work as written.
- **WebSockets:** long-lived connections are supported, so it's ready for future remote play (friend rooms and streamer mode).
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
  BASE_URL = "https://tap-timer.fly.dev"   # change to https://tap-timer.com later
  APP_STORE_ID = "6802904982"
  # Optional public settings (can also go in secrets):
  # GA_MEASUREMENT_ID = "G-XXXXXXX"
  # CONTACT_EMAIL = "you@example.com"

[http_service]
  internal_port = 8080
  force_https = true              # http:// -> https://
  auto_stop_machines = "stop"     # stop idle machines...
  auto_start_machines = true      # ...and start them on the next request
  min_machines_running = 1        # keep one warm: no cold start for visitors
  [http_service.concurrency]
    type = "requests"
    soft_limit = 200
    hard_limit = 250

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

Because pages are identical for everyone and the game runs in the browser, the website itself needs very little. Two machines, for redundancy, can serve a lot of traffic. Putting Cloudflare in front offloads most static requests.

**When remote play arrives:**
- **Problem:** rooms keep state in memory, so all players in a room must reach the same machine. Fly's proxy load-balances across machines, so a second request could land on a machine that doesn't have the room.
- **Fix:** the server that receives a request for a room it doesn't own replies with a `fly-replay: instance=<machine-id>` header, and Fly's proxy transparently re-sends the request to that machine.
  - Encode the owning machine in the room code, or keep a tiny lookup table.
  - Each machine knows its own ID from the `FLY_MACHINE_ID` environment variable.
  - This keeps the simple in-memory design while scaling out. **(verify header syntax)**
- **Scale-to-zero:** with live rooms, set `min_machines_running` to at least the number of room machines and keep `auto_stop_machines` from stopping machines that still hold open connections. Machines with active connections count as busy for the proxy. **(verify)**
- **Shared state:** if rooms ever need to span machines, add Redis (Upstash via `fly redis create`) for pub/sub.

## Checklist

- [ ] `fly auth login`, `fly launch --no-deploy`
- [ ] Replace `fly.toml` (port 8080, `/healthz` check, `BASE_URL=https://<app>.fly.dev`, `force_https`)
- [ ] Add `doc` to `.dockerignore`
- [ ] `fly secrets set` for AdSense, provider token and verification tokens as needed
- [ ] `fly deploy`, then check `/`, `/healthz`, `/robots.txt`, `/get?src=header`, logs
- [ ] Optional: GitHub Actions deploy with `FLY_API_TOKEN`
- [ ] Later: `fly certs add` for the apex and `www`, DNS records, `BASE_URL` → domain, redeploy, SEO checklist
