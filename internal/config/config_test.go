package config

import "testing"

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
