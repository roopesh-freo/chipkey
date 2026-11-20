package api_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"chipkey/api"
)

func locked(c *api.Config) { c.DashUser, c.DashPass = "stage", "let-me-in" }

// browser keeps cookies and follows redirects, like the one on the projector.
func browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func page(t *testing.T, c *http.Client, method, target string, form url.Values) (int, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func signIn(t *testing.T, c *http.Client, base, user, pass string) (int, string) {
	t.Helper()
	return page(t, c, "POST", base+"/login", url.Values{"username": {user}, "password": {pass}})
}

func TestDashboardIsOpenWithoutAPassword(t *testing.T) {
	w := start(t, nil)
	code, body := page(t, http.DefaultClient, "GET", w.url+"/", nil)
	if code != 200 || !strings.Contains(body, "LIVE DECISION LOG") {
		t.Fatalf("dashboard: %d", code)
	}
	if code, _ := page(t, http.DefaultClient, "GET", w.url+"/v1/dash/state", nil); code != 200 {
		t.Fatalf("state: %d", code)
	}
}

func TestDashboardAndStageControlsNeedALogin(t *testing.T) {
	w := start(t, locked)
	c := browser(t)

	code, body := page(t, c, "GET", w.url+"/", nil)
	if code != 200 || !strings.Contains(body, `action="/login"`) || strings.Contains(body, "LIVE DECISION LOG") {
		t.Fatalf("a stranger should get the sign-in page, got %d", code)
	}
	for _, path := range []string{"/v1/dash/state", "/v1/dash/events", "/v1/demo"} {
		if code, body := page(t, c, "GET", w.url+path, nil); code != 401 || !strings.Contains(body, "login_required") {
			t.Fatalf("GET %s without a session: %d %s", path, code, body)
		}
	}
	// The stage controls refuse too, and the night stays as it was.
	resp, err := http.Post(w.url+"/v1/demo", "application/json", strings.NewReader(`{"reset":true,"mode":"legacy"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("POST /v1/demo without a session: %d", resp.StatusCode)
	}

	// The phones' own routes stay open.
	if code, _ := page(t, c, "GET", w.url+"/healthz", nil); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
	if code, _ := page(t, c, "GET", w.url+"/v1/sms?phone=9876543210", nil); code == 401 {
		t.Fatal("a phone route asked for the dashboard login")
	}
}

func TestWrongPasswordIsRefused(t *testing.T) {
	w := start(t, locked)
	c := browser(t)
	for _, try := range [][2]string{{"stage", "wrong"}, {"someone", "let-me-in"}, {"", ""}} {
		code, body := signIn(t, c, w.url, try[0], try[1])
		if code != 401 || !strings.Contains(body, "wrong name or password") {
			t.Fatalf("login as %q: %d", try[0], code)
		}
	}
	if code, _ := page(t, c, "GET", w.url+"/v1/dash/state", nil); code != 401 {
		t.Fatalf("state after failed logins: %d", code)
	}
	if !strings.Contains(w.log.String(), "dashboard login refused") {
		t.Fatal("a refused login should be in the server log")
	}
	if strings.Contains(w.log.String(), "let-me-in") {
		t.Fatal("the password reached the log")
	}
}

func TestLoginStoresASessionInTheBrowser(t *testing.T) {
	w := start(t, locked)
	c := browser(t)

	// The redirect lands on the dashboard itself.
	code, body := signIn(t, c, w.url, "stage", "let-me-in")
	if code != 200 || !strings.Contains(body, "LIVE DECISION LOG") {
		t.Fatalf("after login: %d", code)
	}
	u, _ := url.Parse(w.url)
	cookies := c.Jar.Cookies(u)
	if len(cookies) != 1 || cookies[0].Name != "chipkey_dash" {
		t.Fatalf("cookies: %v", cookies)
	}
	if strings.Contains(cookies[0].Value, "let-me-in") {
		t.Fatal("the password is in the cookie")
	}

	// A reload, the state feed and the stage controls all work on the session.
	if code, body := page(t, c, "GET", w.url+"/", nil); code != 200 || !strings.Contains(body, "LIVE DECISION LOG") {
		t.Fatalf("reload: %d", code)
	}
	code, body = page(t, c, "GET", w.url+"/v1/dash/state", nil)
	if code != 200 || !strings.Contains(body, `"login":true`) {
		t.Fatalf("state: %d %s", code, body)
	}
	req, _ := http.NewRequest("POST", w.url+"/v1/demo", strings.NewReader(`{"mode":"legacy"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stage control with a session: %d", resp.StatusCode)
	}

	// 29 days on it still holds; after 30 it has expired.
	w.now = w.now.Add(29 * 24 * time.Hour)
	if code, _ := page(t, c, "GET", w.url+"/v1/dash/state", nil); code != 200 {
		t.Fatalf("day 29: %d", code)
	}
	w.now = w.now.Add(2 * 24 * time.Hour)
	if code, _ := page(t, c, "GET", w.url+"/v1/dash/state", nil); code != 401 {
		t.Fatalf("day 31: %d", code)
	}
}

func TestSessionSurvivesARestartAndAWipe(t *testing.T) {
	now := time.Now()
	cfg := api.Config{DashUser: "stage", DashPass: "let-me-in", Now: func() time.Time { return now }}
	first := httptest.NewServer(api.New(cfg))
	c := browser(t)
	if code, _ := signIn(t, c, first.URL, "stage", "let-me-in"); code != 200 {
		t.Fatalf("login: %d", code)
	}
	u, _ := url.Parse(first.URL)
	session := c.Jar.Cookies(u)[0]
	first.Close()

	get := func(s *httptest.Server) int {
		req, _ := http.NewRequest("GET", s.URL+"/v1/dash/state", nil)
		req.AddCookie(session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// A new process with the same password honours the stored session.
	second := httptest.NewServer(api.New(cfg))
	defer second.Close()
	if code := get(second); code != 200 {
		t.Fatalf("after a restart: %d", code)
	}
	req, _ := http.NewRequest("POST", second.URL+"/v1/demo", strings.NewReader(`{"reset":true,"mode":"legacy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if code := get(second); resp.StatusCode != 200 || code != 200 {
		t.Fatalf("after a wipe: wipe %d, state %d", resp.StatusCode, code)
	}

	// A new password ends it, and so does a forged expiry.
	cfg.DashPass = "changed"
	third := httptest.NewServer(api.New(cfg))
	defer third.Close()
	if code := get(third); code != 401 {
		t.Fatalf("after a password change: %d", code)
	}
	_, mac, _ := strings.Cut(session.Value, ".")
	session = &http.Cookie{Name: session.Name, Value: "99999999999." + mac}
	if code := get(second); code != 401 {
		t.Fatalf("forged expiry: %d", code)
	}
}

func TestSignOutEndsTheSession(t *testing.T) {
	w := start(t, locked)
	c := browser(t)
	signIn(t, c, w.url, "stage", "let-me-in")

	// The redirect after sign-out lands on the sign-in page.
	code, body := page(t, c, "POST", w.url+"/logout", url.Values{})
	if code != 200 || !strings.Contains(body, `action="/login"`) {
		t.Fatalf("after sign-out: %d", code)
	}
	if code, _ := page(t, c, "GET", w.url+"/v1/dash/state", nil); code != 401 {
		t.Fatalf("state after sign-out: %d", code)
	}
}
