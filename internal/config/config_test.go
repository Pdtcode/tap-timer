package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionBaseURL(t *testing.T) {
	for _, tc := range []struct {
		env, base string
		ok        bool
	}{
		{"development", "http://localhost:8080", true},
		{"production", "https://taptimer.app", true},
		{"production", "http://localhost:8080", false},
		{"production", "https://localhost", false},
		{"production", "http://taptimer.app", false},
	} {
		c := Config{Env: tc.env, BaseURL: tc.base, AppStoreID: "1"}
		if err := c.validate(); (err == nil) != tc.ok {
			t.Errorf("%s %s: err = %v, want ok=%v", tc.env, tc.base, err, tc.ok)
		}
	}
}

func TestStudioValidation(t *testing.T) {
	long := "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name string
		s    Studio
		ok   bool
	}{
		{"off", Studio{}, true},
		{"ok", Studio{Password: "correct horse battery", Secret: long, TZ: "America/Chicago"}, true},
		{"short secret", Studio{Password: "correct horse battery", Secret: "short", TZ: "UTC"}, false},
		{"short password", Studio{Password: "hunter2", Secret: long, TZ: "UTC"}, false},
		{"bad zone", Studio{Password: "correct horse battery", Secret: long, TZ: "Mars/Base"}, false},
	} {
		c := Config{Env: "production", BaseURL: "https://taptimer.app", AppStoreID: "1", Studio: tc.s}
		if err := c.validate(); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestGoogleClientFile(t *testing.T) {
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if id, _, err := googleClientFile(t.TempDir()); id != "" || err != nil {
		t.Errorf("empty dir: %q, %v", id, err)
	}

	dir := t.TempDir()
	write(dir, "client_secret_1.json", `{"web":{"client_id":"abc.apps.googleusercontent.com","client_secret":"shh"}}`)
	if id, secret, err := googleClientFile(dir); id != "abc.apps.googleusercontent.com" || secret != "shh" || err != nil {
		t.Errorf("web client: %q %q %v", id, secret, err)
	}
	write(dir, "client_secret_2.json", `{}`)
	if _, _, err := googleClientFile(dir); err == nil {
		t.Error("two files: no error")
	}

	desktop := t.TempDir()
	write(desktop, "client_secret_1.json", `{"installed":{"client_id":"x","client_secret":"y"}}`)
	if _, _, err := googleClientFile(desktop); err == nil || !strings.Contains(err.Error(), "Desktop") {
		t.Errorf("desktop client: %v", err)
	}
}

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# comment\n\nTT_PLAIN=one\nexport TT_EXPORTED=two\nTT_QUOTED=\"three words\"\nTT_EMPTY=\nTT_ALREADY=from-file\nTT_EQUALS=a=b\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TT_ALREADY", "from-shell")
	for _, k := range []string{"TT_PLAIN", "TT_EXPORTED", "TT_QUOTED", "TT_EMPTY", "TT_EQUALS"} {
		t.Setenv(k, "") // registers cleanup
		os.Unsetenv(k)
	}
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TT_PLAIN": "one", "TT_EXPORTED": "two", "TT_QUOTED": "three words",
		"TT_ALREADY": "from-shell", "TT_EQUALS": "a=b",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, set := os.LookupEnv("TT_EMPTY"); set {
		t.Error("empty value was set")
	}
	if err := loadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
