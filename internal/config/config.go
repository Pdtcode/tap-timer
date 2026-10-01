// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
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
}

func Load() (Config, error) {
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
	}
	for _, name := range AdSlotNames {
		c.AdSlots[name] = os.Getenv("ADSENSE_SLOT_" + strings.ToUpper(name))
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if u, err := url.Parse(c.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("BASE_URL must be an absolute URL, got %q", c.BaseURL)
	}
	if !digitsRe.MatchString(c.AppStoreID) {
		return fmt.Errorf("APP_STORE_ID must be numeric, got %q", c.AppStoreID)
	}
	if c.AdSenseClient != "" && !adClientRe.MatchString(c.AdSenseClient) {
		return fmt.Errorf("ADSENSE_CLIENT must look like ca-pub-1234567890, got %q", c.AdSenseClient)
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
