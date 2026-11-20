package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The dashboard shows every table in the backend and holds the stage
// controls, so once the verifier is reachable from the internet it sits
// behind a login. The phones never use these routes.

//go:embed login.html
var loginHTML []byte

const (
	sessionCookie = "chipkey_dash"
	// A session outlasts rehearsals and the talk, so the dashboard is
	// signed in to once per browser.
	sessionTTL = 30 * 24 * time.Hour
)

func (s *Server) loginOn() bool { return s.cfg.DashPass != "" }

// The session is a signed expiry time, not a row in memory, so it survives
// a restart and a wipe of the demo. Changing the password ends every session.
func (s *Server) signSession(expiry string) string {
	key := sha256.Sum256([]byte("chipkey dashboard session v1\x00" + s.cfg.DashUser + "\x00" + s.cfg.DashPass))
	m := hmac.New(sha256.New, key[:])
	m.Write([]byte(expiry))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) signedIn(r *http.Request) bool {
	if !s.loginOn() {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	expiry, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(s.signSession(expiry))) {
		return false
	}
	until, err := strconv.ParseInt(expiry, 10, 64)
	return err == nil && s.cfg.Now().Unix() < until
}

// staff guards a dashboard API route.
func (s *Server) staff(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.signedIn(r) {
			fail(w, http.StatusUnauthorized, "login_required", "sign in to the dashboard first")
			return
		}
		next(w, r)
	}
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

func (s *Server) loginPage(w http.ResponseWriter, status int, problem string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(bytes.Replace(loginHTML, []byte("<!--problem-->"), []byte(problem), 1))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginOn() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	user, pass := r.PostFormValue("username"), r.PostFormValue("password")
	// Both comparisons always run, so the reply time does not say which was wrong.
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.DashUser))
	okPass := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.DashPass))
	stamp := s.cfg.Now().Format("15:04:05")
	if okUser&okPass != 1 {
		time.Sleep(400 * time.Millisecond) // slows guessing
		fmt.Fprintf(s.cfg.Out, "%s  DASH    ✘ dashboard login refused\n", stamp)
		s.loginPage(w, http.StatusUnauthorized, `<p class="problem" id="problem">✘ wrong name or password</p>`)
		return
	}
	expiry := strconv.FormatInt(s.cfg.Now().Add(sessionTTL).Unix(), 10)
	s.setSession(w, r, expiry+"."+s.signSession(expiry), int(sessionTTL.Seconds()))
	fmt.Fprintf(s.cfg.Out, "%s  DASH    ✔ dashboard login: %s\n", stamp, s.cfg.DashUser)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	// Only a signed-in browser can sign out, so another site cannot do it.
	if s.loginOn() && s.signedIn(r) {
		s.setSession(w, r, "", -1)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
