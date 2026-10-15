// Package config loads runtime settings from environment variables.
package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Ad slot names used by templates. Each maps to an ADSENSE_SLOT_<NAME> env var.
var AdSlotNames = []string{"sidebar", "incontent", "footer"}

var (
	digitsRe   = regexp.MustCompile(`^\d+$`)
	adClientRe = regexp.MustCompile(`^ca-pub-\d+$`)
)

type Config struct {
	Env     string // "development" or "production"
	Port    string
	BaseURL string // public origin, no trailing slash; used for canonical URLs and the sitemap

	AppStoreID string
	// Optional App Store Connect provider token. When set, /get redirects carry
	// pt+ct params so installs are attributed per campaign in App Analytics.
	AppStoreProviderToken string

	AdSenseClient string            // e.g. ca-pub-1234567890123456; empty disables ads
	AdSlots       map[string]string // slot name -> AdSense data-ad-slot id

	GAMeasurementID string // optional GA4 id, e.g. G-XXXXXXX
	ContactEmail    string // shown on the privacy page

	// Optional search-engine ownership tokens, rendered as <meta> verification tags.
	GoogleSiteVerification string
	BingSiteVerification   string

	Studio Studio
}

// Studio configures /studio, the private dashboard for posting Shorts, Reels
// and TikToks. It's off unless STUDIO_PASSWORD is set.
type Studio struct {
	Password string // the one login; set it with `fly secrets set`
	Secret   string // signs session cookies (32+ chars)
	TZ       string // IANA zone for "today" in the streak, e.g. America/Chicago

	YouTubeClientID     string // Google Cloud > APIs & Services > Credentials (OAuth web client)
	YouTubeClientSecret string
	MetaAppID           string // Meta developer app with Facebook Login for Business
	MetaAppSecret       string
	MetaLoginConfigID   string // optional: the Facebook Login for Business configuration to sign in with
	TikTokClientKey     string // TikTok for Developers app with the Content Posting API
	TikTokClientSecret  string

	// Saved platform logins, keyed by the names in TokenEnvs. There's no
	// database: connecting in the studio shows the value to save as a secret.
	Tokens map[string]string
}

// TokenEnvs are the saved logins' variable names. Locally, each can instead
// be a file of that name in SecretsDir (the studio writes it when you connect).
var TokenEnvs = []string{"YOUTUBE_REFRESH_TOKEN", "INSTAGRAM_ACCESS_TOKEN", "TIKTOK_REFRESH_TOKEN"}

func (s Studio) Enabled() bool { return s.Password != "" }

func Load() (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	c := Config{
		Env:                   env("APP_ENV", "development"),
		Port:                  env("PORT", "8080"),
		BaseURL:               strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		AppStoreID:            env("APP_STORE_ID", "6802904982"),
		AppStoreProviderToken: os.Getenv("APP_STORE_PROVIDER_TOKEN"),
		AdSenseClient:         os.Getenv("ADSENSE_CLIENT"),
		AdSlots:               map[string]string{},
		GAMeasurementID:       os.Getenv("GA_MEASUREMENT_ID"),
		ContactEmail:          os.Getenv("CONTACT_EMAIL"),

		GoogleSiteVerification: os.Getenv("GOOGLE_SITE_VERIFICATION"),
		BingSiteVerification:   os.Getenv("BING_SITE_VERIFICATION"),

		Studio: Studio{
			Password:            os.Getenv("STUDIO_PASSWORD"),
			Secret:              os.Getenv("STUDIO_SECRET"),
			TZ:                  env("STUDIO_TZ", "America/Chicago"),
			YouTubeClientID:     os.Getenv("YOUTUBE_CLIENT_ID"),
			YouTubeClientSecret: os.Getenv("YOUTUBE_CLIENT_SECRET"),
			MetaAppID:           os.Getenv("META_APP_ID"),
			MetaAppSecret:       os.Getenv("META_APP_SECRET"),
			MetaLoginConfigID:   os.Getenv("META_LOGIN_CONFIG_ID"),
			TikTokClientKey:     os.Getenv("TIKTOK_CLIENT_KEY"),
			TikTokClientSecret:  os.Getenv("TIKTOK_CLIENT_SECRET"),
			Tokens:              map[string]string{},
		},
	}
	for _, name := range AdSlotNames {
		c.AdSlots[name] = os.Getenv("ADSENSE_SLOT_" + strings.ToUpper(name))
	}
	for _, name := range TokenEnvs {
		v := os.Getenv(name)
		if v == "" {
			b, _ := os.ReadFile(filepath.Join(SecretsDir, name)) // missing is fine
			v = string(b)
		}
		if v = strings.TrimSpace(v); v != "" {
			c.Studio.Tokens[name] = v
		}
	}
	// Locally, a missing STUDIO_SECRET just means a fresh one each run (you
	// sign in again after a restart). Production must set a real one.
	if c.Studio.Enabled() && c.Studio.Secret == "" && !c.IsProduction() {
		b := make([]byte, 32)
		rand.Read(b)
		c.Studio.Secret = hex.EncodeToString(b)
		slog.Warn("STUDIO_SECRET isn't set; using a random one, so restarting signs you out of the studio")
	}
	if c.Studio.YouTubeClientID == "" {
		id, secret, err := googleClientFile(SecretsDir)
		if err != nil {
			return c, err
		}
		c.Studio.YouTubeClientID, c.Studio.YouTubeClientSecret = id, secret
	}
	return c, c.validate()
}

// loadDotEnv sets variables from a local .env file (KEY=value lines; # starts
// a comment). Variables already in the environment win. No file is fine:
// .env is never committed or put in the Docker image, so production never
// reads one.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return fmt.Errorf("%s line %d: expected KEY=value", path, n)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, set := os.LookupEnv(key); set || val == "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}

// SecretsDir holds local-only credentials; git and Docker both ignore it.
const SecretsDir = "secrets"

// googleClientFile reads the OAuth client JSON downloaded from Google Cloud
// (secrets/client_secret_<id>.json), so local runs don't need the
// YOUTUBE_CLIENT_* variables. No file means no client, not an error.
func googleClientFile(dir string) (id, secret string, err error) {
	files, _ := filepath.Glob(filepath.Join(dir, "client_secret_*.json"))
	switch len(files) {
	case 0:
		return "", "", nil
	case 1:
	default:
		return "", "", fmt.Errorf("%s has %d client_secret_*.json files; keep only one", dir, len(files))
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		return "", "", err
	}
	var f struct {
		Web struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"web"`
		Installed json.RawMessage `json:"installed"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return "", "", fmt.Errorf("%s: %w", files[0], err)
	}
	if f.Web.ClientID == "" || f.Web.ClientSecret == "" {
		if f.Installed != nil {
			return "", "", fmt.Errorf("%s is a Desktop client; create a Web application client instead", files[0])
		}
		return "", "", fmt.Errorf("%s has no web client_id and client_secret", files[0])
	}
	return f.Web.ClientID, f.Web.ClientSecret, nil
}

func (c Config) validate() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("BASE_URL must be an absolute URL, got %q", c.BaseURL)
	}
	// Canonical URLs, the sitemap and the host redirect all come from BASE_URL,
	// so a localhost or http value in production would get indexed wrongly.
	if c.IsProduction() && (u.Scheme != "https" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		return fmt.Errorf("in production BASE_URL must be the public https origin, got %q", c.BaseURL)
	}
	if !digitsRe.MatchString(c.AppStoreID) {
		return fmt.Errorf("APP_STORE_ID must be numeric, got %q", c.AppStoreID)
	}
	if c.AdSenseClient != "" && !adClientRe.MatchString(c.AdSenseClient) {
		return fmt.Errorf("ADSENSE_CLIENT must look like ca-pub-1234567890, got %q", c.AdSenseClient)
	}
	if c.Studio.Enabled() {
		if len(c.Studio.Secret) < 32 {
			return fmt.Errorf("STUDIO_SECRET must be at least 32 characters when STUDIO_PASSWORD is set")
		}
		if c.IsProduction() && len(c.Studio.Password) < 12 {
			return fmt.Errorf("STUDIO_PASSWORD must be at least 12 characters in production")
		}
		if _, err := time.LoadLocation(c.Studio.TZ); err != nil {
			return fmt.Errorf("STUDIO_TZ must be an IANA zone like America/Chicago, got %q", c.Studio.TZ)
		}
	}
	return nil
}

func (c Config) IsProduction() bool { return c.Env == "production" }

// AdSensePublisherID is the pub-XXXX form used in ads.txt.
func (c Config) AdSensePublisherID() string { return strings.TrimPrefix(c.AdSenseClient, "ca-") }

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
