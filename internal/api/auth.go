package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/auth"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// Console sign-in: user accounts with server-side sessions in an HttpOnly
// cookie. The cookie authenticates /v1 with full scope and the account
// endpoints under /admin/auth/. API keys and the admin token do not open the
// account endpoints; the admin token only creates the first user.

const (
	sessionCookie = "alertloop_session"
	// consoleHeader must accompany every state-changing request made with the
	// cookie. A form or a link on another site cannot set it.
	consoleHeader = "X-AlertLoop-Console"
	maxAuthBody   = 8 * 1024
	maxUserAgent  = 256
	// maxPasswordChecks bounds the password checks running at once. Each
	// argon2id check holds 19 MiB; unbounded, a burst of sign-ins from a few
	// addresses is enough to run a 1 GB host out of memory.
	maxPasswordChecks = 4
)

// sessionCtxKey carries the signed-in user and session id of a request.
type sessionCtxKey struct{}

type signedIn struct {
	user      *storage.User
	sessionID string
}

func signedInOf(r *http.Request) signedIn {
	v, _ := r.Context().Value(sessionCtxKey{}).(signedIn)
	return v
}

// registerAuth mounts the account endpoints on mux.
func (s *Server) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/auth/me", s.handleMe)
	mux.HandleFunc("POST /admin/auth/setup", s.consoleOnly(s.loginLimit(s.handleSetup)))
	mux.HandleFunc("POST /admin/auth/login", s.consoleOnly(s.loginLimit(s.handleLogin)))
	mux.HandleFunc("POST /admin/auth/logout", s.consoleOnly(s.handleLogout))
	mux.HandleFunc("POST /admin/auth/password", s.consoleOnly(s.withUser(s.handleChangePassword)))
	mux.HandleFunc("GET /admin/auth/users", s.withUser(s.handleListUsers))
	mux.HandleFunc("POST /admin/auth/users", s.consoleOnly(s.withUser(s.handleCreateUser)))
	mux.HandleFunc("POST /admin/auth/users/{id}/disable", s.consoleOnly(s.withUser(s.handleDisableUser)))
	mux.HandleFunc("POST /admin/auth/users/{id}/enable", s.consoleOnly(s.withUser(s.handleEnableUser)))
	mux.HandleFunc("POST /admin/auth/users/{id}/password", s.consoleOnly(s.withUser(s.handleResetPassword)))
}

// sessionOrKey authenticates /v1: a request with an API key or the admin token
// goes to keyAuth as before; one with only the session cookie is checked here
// and gets full scope.
func (s *Server) sessionOrKey(keyAuth, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extractAPIKey(r) != "" {
			keyAuth.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err != nil || c.Value == "" {
			keyAuth.ServeHTTP(w, r)
			return
		}
		in, ok := s.authenticateSession(w, r)
		if !ok {
			return
		}
		if !safeMethod(r.Method) {
			if why := s.notFromConsole(r); why != "" {
				refuseCrossSite(w, why)
				return
			}
		}
		noteCredential(r, "user:"+in.user.ID, config.ScopeFull)
		api.ServeHTTP(w, withCaller(r, caller{scope: config.ScopeFull}))
	})
}

// authenticateSession resolves the session cookie of r. When it is missing or
// no longer valid it clears the cookie, writes 401 and returns false.
func (s *Server) authenticateSession(w http.ResponseWriter, r *http.Request) (signedIn, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in")
		return signedIn{}, false
	}
	id := auth.SessionID(c.Value)
	now := time.Now().UTC()
	u, err := s.store.SessionUser(r.Context(), id, now, now.Add(-auth.IdleTimeout))
	if errors.Is(err, domain.ErrNotFound) {
		s.clearSessionCookie(w, r)
		writeError(w, http.StatusUnauthorized, "unauthorized", "the session has ended; sign in again")
		return signedIn{}, false
	}
	if err != nil {
		writeDomainError(w, err)
		return signedIn{}, false
	}
	return signedIn{user: u, sessionID: id}, true
}

// withUser admits only a signed-in console user.
func (s *Server) withUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := s.authenticateSession(w, r)
		if !ok {
			return
		}
		noteCredential(r, "user:"+in.user.ID, config.ScopeFull)
		next(w, r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, in)))
	}
}

// consoleOnly refuses a state-changing request that did not come from the
// console itself (see notFromConsole).
func (s *Server) consoleOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if why := s.notFromConsole(r); why != "" {
			refuseCrossSite(w, why)
			return
		}
		next(w, r)
	}
}

// loginLimit bounds password guessing per client address, whether or not the
// general rate limit is enabled.
func (s *Server) loginLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loginLimiter.allow(limiterKey(s.trustedProxies.ClientIP(r)), time.Now()) {
			tooManyRequests(w)
			return
		}
		next(w, r)
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// notFromConsole explains why r does not look like a request of the console
// itself, or returns "" when it does: r must carry the console header and,
// when the browser sent an Origin, come from the host the browser reached.
func (s *Server) notFromConsole(r *http.Request) string {
	if r.Header.Get(consoleHeader) != "1" {
		return "a request made with the console session must carry the header " + consoleHeader + ": 1"
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return ""
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return "the request's Origin header is not a valid origin"
	}
	host := s.reachedHost(r)
	if sameHost(u, host) {
		return ""
	}
	return "the page's origin " + u.Scheme + "://" + u.Host + " does not match the host AlertLoop was reached at (" +
		host + "): a reverse proxy must pass the original Host header (nginx: proxy_set_header Host $host; " +
		"Apache: ProxyPreserveHost On), or send X-Forwarded-Host and be listed in rate_limit.trusted_proxies"
}

// reachedHost is the host the browser addressed: X-Forwarded-Host when a
// proxy listed in rate_limit.trusted_proxies sends it, the Host header
// otherwise.
func (s *Server) reachedHost(r *http.Request) string {
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" &&
		s.trustedProxies.trusts(net.ParseIP(hostOnly(r.RemoteAddr))) {
		first, _, _ := strings.Cut(fh, ",")
		return strings.TrimSpace(first)
	}
	return r.Host
}

// sameHost reports whether origin o names host. Host names compare without
// case. The port counts only when host carries one: a proxy that forwards
// "Host: $host" drops the port, and a console served on https://example:8443
// must still work behind it.
func sameHost(o *url.URL, host string) bool {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		h, port = strings.Trim(host, "[]"), ""
	}
	if !strings.EqualFold(o.Hostname(), h) {
		return false
	}
	if port == "" {
		return true
	}
	op := o.Port()
	if op == "" {
		switch strings.ToLower(o.Scheme) {
		case "https":
			op = "443"
		case "http":
			op = "80"
		}
	}
	return op == port
}

func refuseCrossSite(w http.ResponseWriter, why string) {
	writeError(w, http.StatusForbidden, "forbidden", "not a request of the console: "+why)
}

// isHTTPS reports whether the browser reached AlertLoop over HTTPS: directly, or
// through a proxy listed in rate_limit.trusted_proxies that says so in
// X-Forwarded-Proto.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.trustedProxies.trusts(net.ParseIP(hostOnly(r.RemoteAddr))) &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// signInAllowed refuses a password sent over plain HTTP from anywhere but this
// machine or its private network reached directly (an SSH tunnel, Docker's
// port mapping). It writes 403 and returns false.
func (s *Server) signInAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.isHTTPS(r) || demoTokenAllowed(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "https_required",
		"sign-in over plain HTTP is accepted only from this machine or a private network (an SSH tunnel, "+
			"the Compose gateway): open the console over HTTPS through a reverse proxy that is listed in "+
			"rate_limit.trusted_proxies and sends X-Forwarded-Proto: https, or through an SSH tunnel")
	return false
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.MaxLifetime / time.Second),
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// startSession stores a new session for u and sets its cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *storage.User) error {
	token, id, err := auth.NewSessionToken()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	ua := r.UserAgent()
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	if err := s.store.CreateSession(r.Context(), &storage.Session{
		ID: id, UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(auth.MaxLifetime),
		IP: s.trustedProxies.ClientIP(r), UserAgent: ua,
	}); err != nil {
		return err
	}
	u.LastLoginAt = &now
	s.setSessionCookie(w, r, token)
	return nil
}

// userView is a user as the account endpoints return it.
type userView struct {
	ID          string     `json:"id"`
	Login       string     `json:"login"`
	CreatedAt   time.Time  `json:"created_at"`
	DisabledAt  *time.Time `json:"disabled_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

func viewOf(u *storage.User) userView {
	return userView{ID: u.ID, Login: u.Login, CreatedAt: u.CreatedAt, DisabledAt: u.DisabledAt, LastLoginAt: u.LastLoginAt}
}

// decodeAuthBody reads a small JSON body into v, writing 400 when it cannot.
func decodeAuthBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, ok := readBody(w, r, maxAuthBody)
	if !ok {
		return false
	}
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

// writeAccountError maps account errors to responses.
func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, storage.ErrLoginTaken):
		writeError(w, http.StatusConflict, "login_taken", err.Error())
	default:
		writeDomainError(w, err)
	}
}

// handleMe implements GET /admin/auth/me. Signed out, the error code says
// whether the first administrator still has to be created.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		now := time.Now().UTC()
		u, err := s.store.SessionUser(r.Context(), auth.SessionID(c.Value), now, now.Add(-auth.IdleTimeout))
		if err == nil {
			noteCredential(r, "user:"+u.ID, config.ScopeFull)
			writeJSON(w, http.StatusOK, viewOf(u))
			return
		}
		if !errors.Is(err, domain.ErrNotFound) {
			writeDomainError(w, err)
			return
		}
		s.clearSessionCookie(w, r)
	}
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusUnauthorized, "setup_required", "create the first administrator")
		return
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in")
}

type setupRequest struct {
	AdminToken string `json:"admin_token"`
	Login      string `json:"login"`
	Password   string `json:"password"`
}

// handleSetup implements POST /admin/auth/setup: the first administrator,
// created with the admin token while no user exists.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.signInAllowed(w, r) {
		return
	}
	var in setupRequest
	if !decodeAuthBody(w, r, &in) {
		return
	}
	if s.adminToken == "" {
		writeError(w, http.StatusForbidden, "forbidden",
			"admin_token is not set; create the first user with: alertloop user add <login>")
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.AdminToken), []byte(s.adminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin token")
		return
	}
	if s.adminToken == config.DemoAdminToken && !demoTokenAllowed(r) {
		writeError(w, http.StatusForbidden, "forbidden",
			"the public demo admin token change-me-admin is refused through a reverse proxy or from a public address")
		return
	}
	u, err := auth.NewUser(in.Login, in.Password)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if err := s.store.CreateFirstUser(r.Context(), u); err != nil {
		if errors.Is(err, storage.ErrUsersExist) {
			writeError(w, http.StatusConflict, "setup_done", err.Error())
			return
		}
		writeAccountError(w, err)
		return
	}
	s.log.Info("first console administrator created", "user_id", u.ID, "login", u.Login)
	if err := s.startSession(w, r, u); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, viewOf(u))
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// handleLogin implements POST /admin/auth/login. An unknown login, a disabled
// user and a wrong password get the same answer after the same work.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.signInAllowed(w, r) {
		return
	}
	var in loginRequest
	if !decodeAuthBody(w, r, &in) {
		return
	}
	login := strings.ToLower(strings.TrimSpace(in.Login))
	// The same /64 as the per-address limiter: rotating addresses inside it
	// must not buy a fresh set of attempts.
	ip := limiterKey(s.trustedProxies.ClientIP(r))
	if wait := s.throttle.Wait(login, ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second)/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"too many failed sign-ins for this login from this address; try again later")
		return
	}
	var hash string
	u, err := s.store.UserByLogin(r.Context(), login)
	switch {
	case err == nil && u.DisabledAt == nil:
		hash = u.PasswordHash
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		writeDomainError(w, err)
		return
	}
	release, ok := s.acquirePasswordCheck(w)
	if !ok {
		return
	}
	match := auth.CheckPassword(hash, in.Password)
	release()
	if !match {
		s.throttle.Fail(login, ip)
		writeError(w, http.StatusUnauthorized, "unauthorized", "wrong login or password")
		return
	}
	s.throttle.Succeed(login, ip)
	if err := s.startSession(w, r, u); err != nil {
		writeDomainError(w, err)
		return
	}
	noteCredential(r, "user:"+u.ID, config.ScopeFull)
	writeJSON(w, http.StatusOK, viewOf(u))
}

// acquirePasswordCheck takes one of the maxPasswordChecks slots for a
// password check. When all are busy it writes 429 with Retry-After and
// returns false: the request is refused before any hashing.
func (s *Server) acquirePasswordCheck(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case s.passwordChecks <- struct{}{}:
		return func() { <-s.passwordChecks }, true
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"too many sign-ins are being checked at once; try again in a second")
		return nil, false
	}
}

type logoutRequest struct {
	// Everywhere ends every session of the user, not only this one.
	Everywhere bool `json:"everywhere"`
}

// handleLogout implements POST /admin/auth/logout. It always clears the
// cookie; a session that has already ended is not an error.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var in logoutRequest
	if !decodeAuthBody(w, r, &in) {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		id := auth.SessionID(c.Value)
		var err error
		if in.Everywhere {
			now := time.Now().UTC()
			var u *storage.User
			if u, err = s.store.SessionUser(r.Context(), id, now, now.Add(-auth.IdleTimeout)); err == nil {
				noteCredential(r, "user:"+u.ID, config.ScopeFull)
				err = s.store.DeleteUserSessions(r.Context(), u.ID)
			}
		}
		if err == nil || errors.Is(err, domain.ErrNotFound) {
			err = s.store.DeleteSession(r.Context(), id)
		}
		if err != nil {
			writeDomainError(w, err)
			return
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword implements POST /admin/auth/password: the signed-in
// user's own password. Their other sessions end.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	in := signedInOf(r)
	var body changePasswordRequest
	if !decodeAuthBody(w, r, &body) {
		return
	}
	release, ok := s.acquirePasswordCheck(w)
	if !ok {
		return
	}
	match := auth.CheckPassword(in.user.PasswordHash, body.CurrentPassword)
	release()
	if !match {
		writeError(w, http.StatusBadRequest, "validation_failed", "the current password is wrong")
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), in.user.ID, hash, in.sessionID); err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListUsers implements GET /admin/auth/users.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]userView, 0, len(users))
	for i := range users {
		out = append(out, viewOf(&users[i]))
	}
	writeJSON(w, http.StatusOK, listResponse[userView]{Items: out})
}

// handleCreateUser implements POST /admin/auth/users.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if !decodeAuthBody(w, r, &in) {
		return
	}
	u, err := auth.NewUser(in.Login, in.Password)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		writeAccountError(w, err)
		return
	}
	s.log.Info("console user created", "user_id", u.ID, "login", u.Login, "by", "user:"+signedInOf(r).user.ID)
	writeJSON(w, http.StatusCreated, viewOf(u))
}

// handleDisableUser implements POST /admin/auth/users/{id}/disable. The user's
// sessions end at once. Nobody disables themselves: that locks the console
// the moment it is the last account.
func (s *Server) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == signedInOf(r).user.ID {
		writeError(w, http.StatusConflict, "own_account", "you cannot disable your own account")
		return
	}
	if err := s.store.DisableUser(r.Context(), id, time.Now().UTC()); err != nil {
		writeAccountError(w, err)
		return
	}
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	s.log.Info("console user disabled", "user_id", u.ID, "login", u.Login, "by", "user:"+signedInOf(r).user.ID)
	writeJSON(w, http.StatusOK, viewOf(u))
}

// handleEnableUser implements POST /admin/auth/users/{id}/enable: a disabled
// user may sign in again with their password.
func (s *Server) handleEnableUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.EnableUser(r.Context(), id); err != nil {
		writeAccountError(w, err)
		return
	}
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	s.log.Info("console user enabled", "user_id", u.ID, "login", u.Login, "by", "user:"+signedInOf(r).user.ID)
	writeJSON(w, http.StatusOK, viewOf(u))
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// handleResetPassword implements POST /admin/auth/users/{id}/password: an
// administrator sets another user's password, and all of that user's
// sessions end. One's own password changes only through
// /admin/auth/password, which asks for the current one.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	in := signedInOf(r)
	id := r.PathValue("id")
	if id == in.user.ID {
		writeError(w, http.StatusConflict, "own_account",
			"you cannot reset your own password here; use Change password, which asks for the current one")
		return
	}
	var body resetPasswordRequest
	if !decodeAuthBody(w, r, &body) {
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), id, hash, ""); err != nil {
		writeAccountError(w, err)
		return
	}
	s.log.Info("console user password reset", "user_id", id, "by", "user:"+in.user.ID)
	w.WriteHeader(http.StatusNoContent)
}
