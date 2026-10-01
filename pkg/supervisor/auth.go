package supervisor

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/gur-shatz/go-run/internal/log"
)

// authCookieName is deliberately generic — the gate gives away nothing about
// what it protects.
const authCookieName = "session"

// authMaxAge is how long a login stays valid before the cookie's timestamp is
// considered stale and a fresh login is required.
const authMaxAge = 12 * time.Hour

// mfaPendingTTL bounds how long a freshly minted (not yet verified) MFA secret
// stays enrollable before the QR code has to be regenerated.
const mfaPendingTTL = 10 * time.Minute

// authFailLock is how long every credential check stays shut after a failed
// one. It applies gate-wide (not per client), so an attacker who has the
// password gets one guess at the 6-digit code every couple of seconds no
// matter how many connections they open. A var so tests can shorten it.
var authFailLock = 2 * time.Second

// minPasswordLen applies to passwords chosen through the setup form only;
// config-provided passwords are the operator's business.
const minPasswordLen = 8

// authGate is a cookie-session login gate. Unauthenticated requests are
// redirected to a /login form; a correct username and password (plus a TOTP
// code when one is configured) mint a cookie of the form "<unix-ts>.<hash>",
// where hash = sha256(ts|user|password-secret|totp-secret). The configured
// secrets are the only inputs, so the cookie can't be forged without them,
// and timestamps older than authMaxAge are rejected. Validity is recomputed
// from the current credentials on each request, so the gate survives restarts
// and changing the password or the TOTP secret invalidates every outstanding
// cookie.
//
// Credentials come from two places. The config file is authoritative: any
// field it provides is fixed for the life of the process. Whatever it leaves
// empty may be set up through the UI and is persisted in <state_dir>/auth.yml
// (see authStore): with no username/password at all the gate runs in setup
// mode, where every request lands on /setup to create the operator account;
// with credentials but no TOTP secret, /security offers MFA enrolment by QR
// code and the portal shows a nudge banner until it is done.
//
// In-memory state is limited to the last TOTP step accepted (so a code
// sniffed off a login can't be replayed inside its window), the failure lock
// that dampens guessing (see authFailLock), and an in-flight MFA enrolment
// awaiting its first verified code.
type authGate struct {
	hint   string
	store  *authStore // nil when there is nowhere to persist UI-managed values
	logger *log.Logger

	// From config; authoritative when set.
	cfgUser string
	cfgPass string
	cfgTOTP []byte

	mu       sync.Mutex
	file     authFile // UI-managed values, mirrored from the store
	fileTOTP []byte   // decoded file.TOTPSecret
	loadErr  error    // store unreadable: fail closed rather than open
	lastStep int64    // last TOTP step accepted (replay guard)
	pending  *mfaPending

	// attemptMu serialises credential checks so parallel requests can't all
	// slip in before the first failure sets lockedUntil.
	attemptMu   sync.Mutex
	lockedUntil time.Time
}

// authCheck is the outcome of a credential check.
type authCheck int

const (
	authOK     authCheck = iota
	authFailed           // wrong credentials; the gate is now locked for authFailLock
	authLocked           // refused unevaluated: a recent failure still holds the lock
)

// mfaPending is an MFA enrolment that has been shown as a QR code but not yet
// confirmed with a code.
type mfaPending struct {
	secret  string
	key     []byte
	expires time.Time
}

// authCreds is a consistent snapshot of the effective credentials.
type authCreds struct {
	user string
	pass string // clear-text, when the password comes from config
	hash string // pbkdf2 hash, when it comes from the auth file
	totp []byte // nil when MFA is not enabled

	credsFromConfig bool
	totpFromConfig  bool
}

func (this authCreds) configured() bool { return this.user != "" }
func (this authCreds) mfa() bool        { return this.totp != nil }

func newAuthGate(cfg BasicAuthConfig, storePath string, logger *log.Logger) *authGate {
	g := &authGate{
		hint:    cfg.Hint,
		logger:  logger,
		cfgUser: cfg.Username,
		cfgPass: cfg.Password,
	}
	if cfg.TOTPSecret != "" {
		// Config.Validate has already rejected an undecodable secret; a bad
		// one reaching here fails closed (no login possible) rather than open.
		g.cfgTOTP, _ = decodeTOTPSecret(cfg.TOTPSecret)
	}
	if storePath != "" {
		g.store = newAuthStore(storePath)
		f, err := g.store.load()
		if err != nil {
			g.loadErr = err
			if logger != nil {
				logger.Error("auth store %s unreadable: %v; refusing all logins until fixed", storePath, err)
			}
		} else {
			g.setFile(f)
		}
	}
	return g
}

// setFile installs a loaded/saved auth file as the UI-managed credentials.
func (this *authGate) setFile(f authFile) {
	this.file = f
	this.fileTOTP = nil
	if f.TOTPSecret != "" {
		key, err := decodeTOTPSecret(f.TOTPSecret)
		if err != nil && this.logger != nil {
			this.logger.Error("auth store totp_secret undecodable: %v; MFA will refuse every code", err)
		}
		this.fileTOTP = key
	}
}

// creds resolves config over file into one snapshot.
func (this *authGate) creds() authCreds {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.credsLocked()
}

func (this *authGate) credsLocked() authCreds {
	c := authCreds{}
	if this.cfgUser != "" {
		c.user, c.pass, c.credsFromConfig = this.cfgUser, this.cfgPass, true
	} else if this.file.Username != "" {
		c.user, c.hash = this.file.Username, this.file.PasswordHash
	}
	if this.cfgTOTP != nil {
		c.totp, c.totpFromConfig = this.cfgTOTP, true
	} else if this.file.TOTPSecret != "" {
		c.totp = this.fileTOTP
		if c.totp == nil {
			c.totp = []byte{} // undecodable: MFA on, nothing matches
		}
	}
	return c
}

// mfaMissing reports whether the gate has an operator account but no second
// factor yet — the condition the portal banner nudges about.
func (this *authGate) mfaMissing() bool {
	c := this.creds()
	return c.configured() && !c.mfa()
}

// sign returns hex(sha256(ts | user | password-secret | totp-secret)). The
// secrets go last so SHA-256 length-extension can't be used to forge a value
// for a chosen ts.
func (this *authGate) sign(ts int64) string {
	return this.creds().sign(ts)
}

func (this authCreds) sign(ts int64) string {
	secret := this.pass
	if secret == "" {
		secret = this.hash
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%x", ts, this.user, secret, this.totp)))
	return hex.EncodeToString(sum[:])
}

// mint builds the cookie value granted at login time.
func (this *authGate) mint(now time.Time) string {
	ts := now.Unix()
	return fmt.Sprintf("%d.%s", ts, this.sign(ts))
}

// setSessionCookie grants (or refreshes) the browser session.
func (this *authGate) setSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    this.mint(time.Now()),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(authMaxAge.Seconds()),
	})
}

// checkOTP validates a one-time code against the given secret and burns the
// step it matched so the same code is refused a second time. Always true
// when MFA is not enabled.
func (this *authGate) checkOTP(key []byte, code string, now time.Time) bool {
	if key == nil {
		return true
	}
	step := totpMatch(key, code, now)
	if step < 0 {
		return false
	}
	this.mu.Lock()
	defer this.mu.Unlock()
	if step <= this.lastStep {
		return false
	}
	this.lastStep = step
	return true
}

// attempt runs one credential check under the failure lock: while a recent
// failure holds it the check is refused without being evaluated, and a
// failing check (re)starts it. Refused attempts don't extend the lock.
func (this *authGate) attempt(now time.Time, check func() bool) authCheck {
	this.attemptMu.Lock()
	defer this.attemptMu.Unlock()
	if now.Before(this.lockedUntil) {
		return authLocked
	}
	if !check() {
		this.lockedUntil = now.Add(authFailLock)
		return authFailed
	}
	return authOK
}

// checkCredentials validates a full login under the failure lock.
func (this *authGate) checkCredentials(user, pass, code string, now time.Time) authCheck {
	return this.attempt(now, func() bool { return this.credentialsMatch(user, pass, code, now) })
}

// credentialsMatch is the bare check. The password compare is constant-time;
// the OTP is evaluated even on a wrong password so both failures cost the
// same, but a step is only ever burned by a success.
func (this *authGate) credentialsMatch(user, pass, code string, now time.Time) bool {
	c := this.creds()
	if !c.configured() {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(c.user)) == 1
	var passOK bool
	if c.credsFromConfig {
		passOK = subtle.ConstantTimeCompare([]byte(pass), []byte(c.pass)) == 1
	} else {
		passOK = verifyPassword(c.hash, pass)
	}
	if !userOK || !passOK {
		if c.mfa() {
			totpMatch(c.totp, code, now)
		}
		return false
	}
	return this.checkOTP(c.totp, code, now)
}

// middleware gates every route. /login and /logout stay open; any other path
// without a valid session cookie is bounced to the login form with a ?next=
// pointer back to where the request was headed. In setup mode (no operator
// account yet) everything is bounced to /setup instead.
//
// Health probes are also exempt: liveness/readiness probes (e.g. a kubelet
// hitting /backoffice/healthz) carry no session cookie or credentials, so
// gating them would make the orchestrator kill the pod in a crash loop. The
// health endpoints expose no sensitive data, so leaving them open is safe and
// is what every other gated deployment expects.
func (this *authGate) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login", "/logout", "/setup", "/favicon.ico",
			backofficePrefix + "/healthz", backofficePrefix + "/readyz",
			"/healthz", "/readyz":
			next.ServeHTTP(w, r)
			return
		}
		if this.loadErr != nil {
			http.Error(w, "authentication store unreadable; see supervisor log", http.StatusServiceUnavailable)
			return
		}
		if !this.creds().configured() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		if this.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		switch this.basicAuthenticated(r) {
		case authOK:
			next.ServeHTTP(w, r)
			return
		case authLocked:
			// Not a 401: git would take that as wrong credentials and drop
			// the stored password.
			tooManyAttempts(w)
			return
		}
		if r.Header.Get("Authorization") != "" {
			this.basicChallenge(w)
			return
		}
		if wantsBasicChallenge(r) {
			this.basicChallenge(w)
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

// basicAuthenticated checks an HTTP Basic header. When MFA is enabled the
// password must carry the current code as a suffix ("<password><6 digits>"),
// since Basic has no third field. Each Basic request is a fresh login, so the
// replay guard applies: a given code authenticates one request. Without an
// Authorization header the result is authFailed but no lock is taken.
func (this *authGate) basicAuthenticated(r *http.Request) authCheck {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return authFailed
	}
	code := ""
	if this.creds().mfa() && len(pass) >= totpDigits {
		pass, code = pass[:len(pass)-totpDigits], pass[len(pass)-totpDigits:]
	}
	return this.checkCredentials(user, pass, code, time.Now())
}

// tooManyAttempts answers a credential check refused by the failure lock.
func tooManyAttempts(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int((authFailLock+time.Second-1)/time.Second)))
	http.Error(w, "Too many failed attempts; try again in a few seconds", http.StatusTooManyRequests)
}

func (this *authGate) basicChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="supervisor"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

func wantsBasicChallenge(r *http.Request) bool {
	if strings.Contains(strings.ToLower(r.UserAgent()), "git/") {
		return true
	}
	accept := r.Header.Get("Accept")
	return accept != "" && !strings.Contains(strings.ToLower(accept), "text/html")
}

// ---- login -----------------------------------------------------------------

// loginPage serves the login form (GET /login). An already-authenticated
// visitor is sent straight on to their destination; in setup mode there is
// nothing to log in to yet.
func (this *authGate) loginPage(w http.ResponseWriter, r *http.Request) {
	if !this.creds().configured() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if this.authenticated(r) {
		http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
		return
	}
	this.renderLogin(w, r, loginFresh, http.StatusOK)
}

// loginSubmit validates the posted credentials (POST /login). On success it
// sets the session cookie and redirects to ?next= (or "/"); on failure it
// re-renders the form with an error.
func (this *authGate) loginSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	user := r.PostForm.Get("username")
	pass := r.PostForm.Get("password")
	code := r.PostForm.Get("otp")
	switch this.checkCredentials(user, pass, code, time.Now()) {
	case authLocked:
		w.Header().Set("Retry-After", strconv.Itoa(int((authFailLock+time.Second-1)/time.Second)))
		this.renderLogin(w, r, loginLocked, http.StatusTooManyRequests)
		return
	case authFailed:
		this.renderLogin(w, r, loginFailed, http.StatusUnauthorized)
		return
	}
	this.setSessionCookie(w)
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

type loginView struct {
	Hint   string
	Next   string
	Failed bool
	Locked bool // refused by the failure lock
	OTP    bool // render the one-time code field
}

// loginOutcome picks the message the login form is re-rendered with.
type loginOutcome int

const (
	loginFresh loginOutcome = iota
	loginFailed
	loginLocked
)

func (this *authGate) renderLogin(w http.ResponseWriter, r *http.Request, outcome loginOutcome, status int) {
	this.render(w, "login", status, loginView{
		Hint:   this.hint,
		Next:   safeNext(r.FormValue("next")),
		Failed: outcome == loginFailed,
		Locked: outcome == loginLocked,
		OTP:    this.creds().mfa(),
	})
}

// ---- first-run setup -------------------------------------------------------

type setupView struct {
	Error    string
	Username string
	ReadOnly bool // no writable store: setup can't be completed here
}

// setupPage (GET /setup) creates the operator account when neither config nor
// the auth file provides one. Once an account exists the page is gone.
func (this *authGate) setupPage(w http.ResponseWriter, r *http.Request) {
	if this.creds().configured() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	this.render(w, "setup", http.StatusOK, setupView{ReadOnly: this.store == nil})
}

// setupSubmit (POST /setup) validates, persists and logs the new account in,
// then lands on /security so MFA enrolment is the obvious next step.
func (this *authGate) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if this.creds().configured() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	user := strings.TrimSpace(r.PostForm.Get("username"))
	pass := r.PostForm.Get("password")
	confirm := r.PostForm.Get("confirm")
	fail := func(msg string) {
		this.render(w, "setup", http.StatusBadRequest, setupView{Error: msg, Username: user, ReadOnly: this.store == nil})
	}
	switch {
	case this.store == nil:
		fail("This supervisor has no writable state directory, so an account can't be saved here. Provide basic_auth.username and password in the config instead.")
		return
	case user == "":
		fail("Username is required.")
		return
	case len(pass) < minPasswordLen:
		fail(fmt.Sprintf("Password must be at least %d characters.", minPasswordLen))
		return
	case pass != confirm:
		fail("Passwords do not match.")
		return
	}
	hash, err := hashPassword(pass)
	if err != nil {
		fail("Could not hash the password: " + err.Error())
		return
	}

	this.mu.Lock()
	if this.credsLocked().configured() { // lost a race with another setup
		this.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	f := this.file
	f.Username, f.PasswordHash = user, hash
	err = this.store.save(f)
	if err == nil {
		this.setFile(f)
	}
	this.mu.Unlock()
	if err != nil {
		fail("Could not save the account: " + err.Error())
		return
	}
	if this.logger != nil {
		this.logger.Status("auth: operator account %q created via setup", user)
	}
	this.setSessionCookie(w)
	http.Redirect(w, r, "/security", http.StatusSeeOther)
}

// ---- security page / MFA enrolment ----------------------------------------

type securityView struct {
	Username        string
	CredsFromConfig bool
	MFA             bool
	MFAFromConfig   bool
	ReadOnly        bool
	Notice          string
	Error           string
}

// securityPage (GET /security) shows the account and MFA status with the
// enrol / disable actions that apply.
func (this *authGate) securityPage(w http.ResponseWriter, r *http.Request) {
	c := this.creds()
	this.render(w, "security", http.StatusOK, securityView{
		Username:        c.user,
		CredsFromConfig: c.credsFromConfig,
		MFA:             c.mfa(),
		MFAFromConfig:   c.totpFromConfig,
		ReadOnly:        this.store == nil,
		Notice:          r.URL.Query().Get("notice"),
		Error:           r.URL.Query().Get("error"),
	})
}

type mfaView struct {
	QR     template.URL // data: URI of the QR PNG; typed so html/template keeps it
	Secret string       // spaced base32 for manual entry
	URI    string
	Error  string
}

// mfaPage (GET /security/mfa) mints (or reuses) a pending secret and shows it
// as a QR code. Nothing is enabled until a code from the app verifies.
func (this *authGate) mfaPage(w http.ResponseWriter, r *http.Request) {
	if c := this.creds(); c.mfa() {
		http.Redirect(w, r, "/security", http.StatusSeeOther)
		return
	}
	if this.store == nil {
		http.Redirect(w, r, "/security?error="+url.QueryEscape("No writable state directory: set basic_auth.totp_secret in the config instead."), http.StatusSeeOther)
		return
	}
	p, err := this.pendingMFA()
	if err != nil {
		http.Error(w, "mint secret: "+err.Error(), http.StatusInternalServerError)
		return
	}
	this.renderMFA(w, p, "", http.StatusOK)
}

// pendingMFA returns the in-flight enrolment, minting a fresh one when there
// is none or the old one expired.
func (this *authGate) pendingMFA() (*mfaPending, error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.pending != nil && time.Now().Before(this.pending.expires) {
		return this.pending, nil
	}
	secret, err := newTOTPSecret()
	if err != nil {
		return nil, err
	}
	key, _ := decodeTOTPSecret(secret)
	this.pending = &mfaPending{secret: secret, key: key, expires: time.Now().Add(mfaPendingTTL)}
	return this.pending, nil
}

func (this *authGate) renderMFA(w http.ResponseWriter, p *mfaPending, errMsg string, status int) {
	uri := totpURI("supervisor", this.creds().user, p.secret)
	png, err := qrcode.Encode(uri, qrcode.Medium, 220)
	if err != nil {
		http.Error(w, "render QR: "+err.Error(), http.StatusInternalServerError)
		return
	}
	this.render(w, "mfa", status, mfaView{
		QR:     template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)),
		Secret: groupSecret(p.secret),
		URI:    uri,
		Error:  errMsg,
	})
}

// mfaSubmit (POST /security/mfa) verifies the first code against the pending
// secret; only then is MFA switched on and persisted. The session cookie is
// re-minted because enabling MFA changes the signing input.
func (this *authGate) mfaSubmit(w http.ResponseWriter, r *http.Request) {
	if this.creds().mfa() || this.store == nil {
		http.Redirect(w, r, "/security", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	code := r.PostForm.Get("otp")

	this.mu.Lock()
	p := this.pending
	this.mu.Unlock()
	if p == nil || time.Now().After(p.expires) {
		http.Redirect(w, r, "/security/mfa", http.StatusSeeOther)
		return
	}
	step := totpMatch(p.key, code, time.Now())
	if step < 0 {
		this.renderMFA(w, p, "That code didn't match. Check the app shows this supervisor and try the next code.", http.StatusBadRequest)
		return
	}

	this.mu.Lock()
	f := this.file
	f.TOTPSecret = p.secret
	if err := this.store.save(f); err != nil {
		this.mu.Unlock()
		this.renderMFA(w, p, "Could not save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	this.setFile(f)
	this.pending = nil
	this.lastStep = step
	this.mu.Unlock()
	if this.logger != nil {
		this.logger.Status("auth: MFA enabled via security page")
	}
	this.setSessionCookie(w)
	http.Redirect(w, r, "/security?notice="+url.QueryEscape("Two-factor authentication is on. Keep the authenticator entry; you will need a code at every login."), http.StatusSeeOther)
}

// mfaDisable (POST /security/mfa/disable) turns a UI-enrolled second factor
// off again, given a current code. A config-provided secret can't be
// disabled here.
func (this *authGate) mfaDisable(w http.ResponseWriter, r *http.Request) {
	c := this.creds()
	if !c.mfa() || c.totpFromConfig || this.store == nil {
		http.Redirect(w, r, "/security", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	now := time.Now()
	code := r.PostForm.Get("otp")
	if this.attempt(now, func() bool { return this.checkOTP(c.totp, code, now) }) != authOK {
		http.Redirect(w, r, "/security?error="+url.QueryEscape("Enter a current code to turn two-factor authentication off."), http.StatusSeeOther)
		return
	}
	this.mu.Lock()
	f := this.file
	f.TOTPSecret = ""
	if err := this.store.save(f); err != nil {
		this.mu.Unlock()
		http.Redirect(w, r, "/security?error="+url.QueryEscape("Could not save: "+err.Error()), http.StatusSeeOther)
		return
	}
	this.setFile(f)
	this.mu.Unlock()
	if this.logger != nil {
		this.logger.Status("auth: MFA disabled via security page")
	}
	this.setSessionCookie(w)
	http.Redirect(w, r, "/security?notice="+url.QueryEscape("Two-factor authentication is off."), http.StatusSeeOther)
}

// groupSecret spaces a base32 secret in fours for manual entry.
func groupSecret(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (this *authGate) render(w http.ResponseWriter, name string, status int, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := authTemplates.ExecuteTemplate(w, name, data); err != nil && this.logger != nil {
		this.logger.Error("render %s: %v", name, err)
	}
}

// safeNext sanitises a post-login redirect target so it can only point at a
// path on this server — guarding against open-redirect via ?next=//evil.com.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

//go:embed auth.html
var authTemplateHTML string

// authTemplates holds the unbranded centered-card pages: login, setup,
// security and mfa. html/template escapes every operator-supplied string.
var authTemplates = template.Must(template.New("auth").Parse(authTemplateHTML))
