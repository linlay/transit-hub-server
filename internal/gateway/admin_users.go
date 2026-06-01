package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/linlay/transit-hub/internal/store"
)

type createAdminUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

type patchAdminUserRequest struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	Status   *string `json:"status"`
}

func (g *Gateway) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := g.store.ListAdminUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func (g *Gateway) createAdminUser(w http.ResponseWriter, r *http.Request) {
	var req createAdminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	user, err := g.store.CreateAdminUser(r.Context(), store.CreateAdminUserParams{
		Username: req.Username,
		Password: req.Password,
		Status:   req.Status,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (g *Gateway) patchAdminUser(w http.ResponseWriter, r *http.Request) {
	var req patchAdminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	user, err := g.store.UpdateAdminUser(r.Context(), chi.URLParam(r, "id"), store.AdminUserPatch{
		Username: req.Username,
		Password: req.Password,
		Status:   req.Status,
	})
	if errors.Is(err, store.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "admin user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (g *Gateway) deleteAdminUser(w http.ResponseWriter, r *http.Request) {
	user, err := g.store.DisableAdminUser(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrUserNotFound) {
		writeError(w, http.StatusNotFound, "admin user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}
