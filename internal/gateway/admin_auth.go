package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/linlay/transit-hub/internal/store"
)

const adminSessionCookieName = "transit_hub_session"

type adminUserContextKey struct{}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (g *Gateway) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	user, err := g.store.VerifyAdminLogin(r.Context(), req.Username, req.Password)
	if errors.Is(err, store.ErrUserNotFound) || errors.Is(err, store.ErrInvalidPassword) || errors.Is(err, store.ErrUserInactive) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session, err := g.store.CreateAdminSession(r.Context(), user.ID, g.env.AdminSessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, g.adminSessionCookie(session.Token, session.ExpiresAt))
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (g *Gateway) me(w http.ResponseWriter, r *http.Request) {
	if user, ok := adminUserFromContext(r.Context()); ok {
		writeJSON(w, http.StatusOK, map[string]any{"user": user})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"id": "admin-token", "username": "admin-token", "status": "active"}})
}

func (g *Gateway) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookieName); err == nil && cookie.Value != "" {
		_ = g.store.DeleteAdminSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, g.expiredAdminSessionCookie())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (g *Gateway) authenticateAdminSession(r *http.Request) (store.AdminUser, bool) {
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil || cookie.Value == "" {
		return store.AdminUser{}, false
	}
	user, err := g.store.AuthenticateAdminSession(r.Context(), cookie.Value, time.Now().UTC())
	if err != nil {
		return store.AdminUser{}, false
	}
	return user, true
}

func (g *Gateway) adminSessionCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   g.env.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (g *Gateway) expiredAdminSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.env.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func withAdminUser(ctx context.Context, user store.AdminUser) context.Context {
	return context.WithValue(ctx, adminUserContextKey{}, user)
}

func adminUserFromContext(ctx context.Context) (store.AdminUser, bool) {
	user, ok := ctx.Value(adminUserContextKey{}).(store.AdminUser)
	return user, ok
}
