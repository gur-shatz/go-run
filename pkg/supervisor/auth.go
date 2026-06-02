package supervisor

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// authCookieName is deliberately generic — the gate gives away nothing about
// what it protects.
const authCookieName = "session"

// authMaxAge is how long a login stays valid before the cookie's timestamp is
// considered stale and a fresh login is required.
const authMaxAge = 12 * time.Hour

// authGate is a cookie-session login gate. Unauthenticated requests are
// redirected to a /login form; a correct username and password mint a cookie
// of the form "<unix-ts>.<hash>", where hash = sha256(ts|user|password). The
// password is the only secret, so the cookie can't be forged without it, and
// timestamps older than authMaxAge are rejected. Nothing server-side is stored:
// validity is recomputed from the configured credentials on each request, so
// the gate survives restarts and changing the password invalidates every
// outstanding cookie.
type authGate struct {
	user string
	pass string
	hint string // optional credentials hint shown under the form
}

func newAuthGate(cfg BasicAuthConfig) *authGate {
	return &authGate{
		user: cfg.Username,
		pass: cfg.Password,
		hint: cfg.Hint,
	}
}

// sign returns hex(sha256(ts | user | password)). The password goes last so
// SHA-256 length-extension can't be used to forge a value for a chosen ts.
func (this *authGate) sign(ts int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s", ts, this.user, this.pass)))
	return hex.EncodeToString(sum[:])
}

// mint builds the cookie value granted at login time.
func (this *authGate) mint(now time.Time) string {
	ts := now.Unix()
	return fmt.Sprintf("%d.%s", ts, this.sign(ts))
}

// middleware gates every route. /login and /logout stay open; any other path
// without a valid session cookie is bounced to the login form with a ?next=
// pointer back to where the request was headed.
func (this *authGate) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login", "/logout":
			next.ServeHTTP(w, r)
			return
		}
		if this.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Absolute "/login": http.Redirect resolves a relative target against
		// the request's directory, which would mis-send /backoffice/* requests
		// to /backoffice/login. The form itself posts back with a relative
		// action so the round-trip stays correct behind a prefix.
		target := "/login"
		if nxt := safeNext(r.URL.RequestURI()); nxt != "/" {
			target += "?next=" + url.QueryEscape(nxt)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}

// authenticated reports whether the request carries a valid, unexpired cookie.
func (this *authGate) authenticated(r *http.Request) bool {
	c, err := r.Cookie(authCookieName)
	if err != nil {
		return false
	}
	tsStr, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	if time.Since(time.Unix(ts, 0)) > authMaxAge {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sig), []byte(this.sign(ts))) == 1
}

// loginPage serves the login form (GET /login). An already-authenticated
// visitor is sent straight on to their destination.
func (this *authGate) loginPage(w http.ResponseWriter, r *http.Request) {
	if this.authenticated(r) {
		http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
		return
	}
	this.render(w, r, false, http.StatusOK)
}

// loginSubmit validates the posted credentials (POST /login). On success it
// sets the session cookie and redirects to ?next= (or "/"); on failure it
// re-renders the form with an error.
func (this *authGate) loginSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	user := r.PostForm.Get("username")
	pass := r.PostForm.Get("password")
	if subtle.ConstantTimeCompare([]byte(user), []byte(this.user)) != 1 ||
		subtle.ConstantTimeCompare([]byte(pass), []byte(this.pass)) != 1 {
		this.render(w, r, true, http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    this.mint(time.Now()),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(authMaxAge.Seconds()),
	})
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// logout clears the session cookie and returns to the login form.
func (this *authGate) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// loginView is the template data for the login page.
type loginView struct {
	Hint   string
	Next   string
	Failed bool
}

func (this *authGate) render(w http.ResponseWriter, r *http.Request, failed bool, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, loginView{
		Hint:   this.hint,
		Next:   safeNext(r.FormValue("next")),
		Failed: failed,
	})
}

// safeNext sanitises a post-login redirect target so it can only point at a
// path on this server — guarding against open-redirect via ?next=//evil.com.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

// loginTemplate is an unbranded but presentable centered-card login form.
// html/template escapes the hint and next value, so operator-supplied strings
// are inert.
var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center;
         font: 15px/1.5 system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
         background: #f4f5f7; color: #1f2329; }
  .card { width: 320px; padding: 32px 28px; border-radius: 12px; background: #fff;
          box-shadow: 0 1px 3px rgba(0,0,0,.08), 0 8px 24px rgba(0,0,0,.08); }
  h1 { margin: 0 0 20px; font-size: 20px; font-weight: 600; text-align: center; }
  label { display: block; font-size: 13px; font-weight: 500; margin-bottom: 14px; color: #444b54; }
  input { width: 100%; margin-top: 6px; padding: 10px 12px; font-size: 14px;
          border: 1px solid #d0d4da; border-radius: 8px; background: #fff; color: inherit; }
  input:focus { outline: none; border-color: #2d6cdf; box-shadow: 0 0 0 3px rgba(45,108,223,.15); }
  button { width: 100%; margin-top: 8px; padding: 11px; font-size: 15px; font-weight: 600;
           color: #fff; background: #2d6cdf; border: 0; border-radius: 8px; cursor: pointer; }
  button:hover { background: #2a61c6; }
  .err { margin: 0 0 16px; padding: 10px 12px; font-size: 13px; border-radius: 8px;
         color: #a1271b; background: #fdecea; }
  .hint { margin: 18px 0 0; font-size: 12px; text-align: center; color: #707880; }
  @media (prefers-color-scheme: dark) {
    body { background: #15171c; color: #e6e8eb; }
    .card { background: #20242b; box-shadow: 0 1px 3px rgba(0,0,0,.4), 0 8px 24px rgba(0,0,0,.5); }
    label { color: #aeb4bd; }
    input { background: #181b20; border-color: #353b44; }
    .err { color: #f3b5ad; background: #3a201d; }
  }
</style>
</head>
<body>
<form class="card" method="post" action="login">
  <h1>Sign in</h1>
  {{if .Failed}}<p class="err">Incorrect username or password.</p>{{end}}
  <input type="hidden" name="next" value="{{.Next}}">
  <label>Username
    <input name="username" autocomplete="username" autofocus required>
  </label>
  <label>Password
    <input name="password" type="password" autocomplete="current-password" required>
  </label>
  <button type="submit">Sign in</button>
  {{if .Hint}}<p class="hint">{{.Hint}}</p>{{end}}
</form>
</body>
</html>
`))
