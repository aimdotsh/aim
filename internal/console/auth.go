package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type contextKey string

const userContextKey contextKey = "aim-user"

type loginAttempt struct {
	Count int
	Since time.Time
}

type Auth struct {
	Store        *Store
	CookieSecure bool
	mu           sync.Mutex
	attempts     map[string]loginAttempt
}

func NewAuth(store *Store, cookieSecure bool) *Auth {
	return &Auth{Store: store, CookieSecure: cookieSecure, attempts: make(map[string]loginAttempt)}
}

func UserFromContext(ctx context.Context) *User {
	user, _ := ctx.Value(userContextKey).(*User)
	return user
}

func remoteIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" && net.ParseIP(forwarded) != nil {
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &input, 16<<10); err != nil {
		return
	}
	ip := remoteIP(r)
	if a.rateLimited(ip) {
		writeError(w, http.StatusTooManyRequests, "登录失败次数过多，请稍后重试")
		return
	}
	var user User
	var passwordHash string
	var active int
	err := a.Store.DB.QueryRowContext(r.Context(), `SELECT id,username,password_hash,role,active FROM users WHERE username=?`, strings.TrimSpace(input.Username)).
		Scan(&user.ID, &user.Username, &passwordHash, &user.Role, &active)
	if err != nil || active != 1 || !VerifyPassword(passwordHash, input.Password) {
		a.recordFailure(ip)
		a.Store.Audit(r.Context(), nil, ip, "login_failed", "session", "", `{}`)
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	user.Active = true
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	now, expires := time.Now().UTC(), time.Now().UTC().Add(12*time.Hour)
	_, err = a.Store.DB.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at,created_at,remote_addr,user_agent) VALUES(?,?,?,?,?,?,?)`,
		tokenHash(token), user.ID, csrf, expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), ip, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	a.clearFailures(ip)
	http.SetCookie(w, &http.Cookie{Name: "aim_session", Value: token, Path: "/", HttpOnly: true, Secure: a.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds())})
	a.Store.Audit(r.Context(), &user, ip, "login", "session", "", `{}`)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrf_token": csrf})
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("aim_session"); err == nil {
		_, _ = a.Store.DB.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=?`, tokenHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "aim_session", Value: "", Path: "/", HttpOnly: true, Secure: a.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	user := UserFromContext(r.Context())
	a.Store.Audit(r.Context(), user, remoteIP(r), "logout", "session", "", `{}`)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("aim_session")
		if err != nil {
			writeError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var user User
		var active int
		var csrf, expires string
		err = a.Store.DB.QueryRowContext(r.Context(), `SELECT u.id,u.username,u.role,u.active,s.csrf_token,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, tokenHash(cookie.Value)).
			Scan(&user.ID, &user.Username, &user.Role, &active, &csrf, &expires)
		if err != nil || active != 1 {
			writeError(w, http.StatusUnauthorized, "会话已失效")
			return
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, expires)
		if err != nil || time.Now().UTC().After(expiresAt) {
			_, _ = a.Store.DB.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=?`, tokenHash(cookie.Value))
			writeError(w, http.StatusUnauthorized, "会话已过期")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && r.Header.Get("X-CSRF-Token") != csrf {
			writeError(w, http.StatusForbidden, "CSRF 校验失败")
			return
		}
		user.Active = true
		ctx := context.WithValue(r.Context(), userContextKey, &user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, role := range roles {
		allowed[role] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFromContext(r.Context())
			if user == nil || !allowed[user.Role] {
				writeError(w, http.StatusForbidden, "权限不足")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *Auth) Current(w http.ResponseWriter, r *http.Request) {
	var csrf string
	cookie, _ := r.Cookie("aim_session")
	_ = a.Store.DB.QueryRowContext(r.Context(), `SELECT csrf_token FROM sessions WHERE token_hash=?`, tokenHash(cookie.Value)).Scan(&csrf)
	writeJSON(w, http.StatusOK, map[string]any{"user": UserFromContext(r.Context()), "csrf_token": csrf})
}

func (a *Auth) rateLimited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts[ip]
	if time.Since(attempt.Since) > 15*time.Minute {
		delete(a.attempts, ip)
		return false
	}
	return attempt.Count >= 5
}

func (a *Auth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts[ip]
	if attempt.Since.IsZero() || time.Since(attempt.Since) > 15*time.Minute {
		attempt = loginAttempt{Since: time.Now()}
	}
	attempt.Count++
	a.attempts[ip] = attempt
}

func (a *Auth) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func scanNullableTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &t
}
