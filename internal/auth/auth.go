package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mtimpe/httpmux/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const cookieName = "httpmux_session"

type Authenticator struct {
	users  []config.User
	secret []byte
	maxAge int
}

func New(cfg config.AuthConfig) *Authenticator {
	return &Authenticator{
		users:  cfg.Users,
		secret: []byte(cfg.Session.Secret),
		maxAge: cfg.Session.MaxAge,
	}
}

func (a *Authenticator) Authenticate(username, password string) bool {
	for _, u := range a.users {
		if u.Username == username {
			err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password))
			return err == nil
		}
	}
	return false
}

func (a *Authenticator) SetSession(w http.ResponseWriter, username string) {
	expiry := time.Now().Add(time.Duration(a.maxAge) * time.Second).Unix()
	value := fmt.Sprintf("%s|%d", username, expiry)
	sig := a.sign(value)
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    value + "|" + sig,
		Path:     "/",
		MaxAge:   a.maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	}
	http.SetCookie(w, cookie)
}

func (a *Authenticator) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

func (a *Authenticator) ValidateRequest(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}

	parts := strings.SplitN(cookie.Value, "|", 3)
	if len(parts) != 3 {
		return "", false
	}

	username := parts[0]
	expiryStr := parts[1]
	sig := parts[2]

	if a.sign(username+"|"+expiryStr) != sig {
		return "", false
	}

	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix() > expiry {
		return "", false
	}

	return username, true
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.ValidateRequest(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) sign(value string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
