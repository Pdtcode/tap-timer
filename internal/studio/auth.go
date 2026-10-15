package studio

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	sessionCookie = "studio_session"
	stateCookie   = "studio_oauth_state"
	sessionTTL    = 30 * 24 * time.Hour // stay signed in on your phone

	loginFailLimit  = 5
	loginFailWindow = 15 * time.Minute
)

// loginLimiter counts failed logins per IP so the password can't be guessed
// quickly. In memory: a restart resets it, which is fine at this rate.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func (l *loginLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.fails[ip][:0]
	for _, t := range l.fails[ip] {
		if now.Sub(t) < loginFailWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.fails, ip)
	} else {
		l.fails[ip] = recent
	}
	return len(recent) >= loginFailLimit
}

func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.fails[ip], now)
}

func (s *Studio) passwordOK(given string) bool {
	a := sha256.Sum256([]byte(given))
	b := sha256.Sum256([]byte(s.cfg.Studio.Password))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// session cookies are <expiry unix, 8 bytes><hmac>, base64url.
func (s *Studio) newSession(now time.Time) string {
	msg := binary.BigEndian.AppendUint64(nil, uint64(now.Add(sessionTTL).Unix()))
	return base64.RawURLEncoding.EncodeToString(append(msg, s.signer.sign(msg)...))
}

func (s *Studio) validSession(v string, now time.Time) bool {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	msg, mac := raw[:8], raw[8:]
	if !hmac.Equal(mac, s.signer.sign(msg)) {
		return false
	}
	return now.Unix() < int64(binary.BigEndian.Uint64(msg))
}

func (s *Studio) setCookie(c *gin.Context, name, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/studio",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cfg.IsProduction(),
		// Lax, not Strict: the OAuth callback is a top-level redirect from
		// Google, Facebook or TikTok and must carry the cookie.
		SameSite: http.SameSiteLaxMode,
	})
}

// requireAuth sends signed-out visitors to the login page (or 401s the API).
func (s *Studio) requireAuth(c *gin.Context) {
	if v, err := c.Cookie(sessionCookie); err == nil && s.validSession(v, time.Now()) {
		c.Next()
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/studio/api/") {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "signed out"})
		return
	}
	c.Redirect(http.StatusSeeOther, "/studio/login")
	c.Abort()
}

// sameOrigin rejects cross-site form posts and API calls (CSRF). Browsers
// send Origin on every POST/PUT/DELETE.
func (s *Studio) sameOrigin(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead:
		c.Next()
		return
	}
	base, _ := url.Parse(s.cfg.BaseURL) // validated at startup
	o, err := url.Parse(c.GetHeader("Origin"))
	if err != nil || o.Scheme != base.Scheme || o.Host != base.Host {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Next()
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b) // never fails (crypto/rand panics on a broken system)
	return base64.RawURLEncoding.EncodeToString(b)
}
