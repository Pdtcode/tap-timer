# secrets/

Local-only credentials. Everything in this folder except this README is
ignored by git (`.gitignore`) and left out of the Docker image
(`.dockerignore`), so it never reaches GitHub or Fly.

- **Google OAuth client** for the studio's YouTube connection: put the JSON
  you downloaded from Google Cloud (`client_secret_<id>.json`) here. When
  `YOUTUBE_CLIENT_ID` isn't set, the app reads the client ID and secret from
  it at startup. Keep only one `client_secret_*.json` here.

- **Saved logins** (`YOUTUBE_REFRESH_TOKEN`, ...): the studio writes these
  here when you connect a platform on a local run, so you stay connected
  across restarts.

In production, set the same values with `fly secrets set` instead.
