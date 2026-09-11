package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/linlay/transit-hub/internal/store"
)

func (g *Gateway) bindAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if g.accessKeys == nil {
		writeError(w, http.StatusServiceUnavailable, "access token binding is not configured")
		return
	}
	identity, err := g.accessKeys.Verify(bearerToken(r.Header.Get("Authorization")), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid access token")
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	if req.DeviceID == "" || len(req.DeviceID) > 256 || strings.IndexFunc(req.DeviceID, unicode.IsControl) >= 0 || len(req.Name) > 256 {
		writeError(w, http.StatusBadRequest, "invalid device_id or name")
		return
	}
	// Validate the issuance policy without allowing the caller to choose privileges.
	if _, err = g.validateAllowedModels(g.accessKeys.Config.AllowedModels); err != nil {
		writeError(w, http.StatusServiceUnavailable, "device key model policy unavailable")
		return
	}
	result, created, err := g.store.BindAPIKey(r.Context(), g.accessKeys, identity, req.DeviceID, req.Name, time.Now().UTC())
	if errors.Is(err, store.ErrDeviceKeyUnavailable) {
		writeError(w, http.StatusConflict, "bound api key is disabled, deleted or expired")
		return
	}
	if err != nil {
		g.logger.Printf("bind-apikey storage operation failed")
		writeError(w, http.StatusInternalServerError, "api key binding failed")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, createAPIKeyResponse{apiKeyResponse: toAPIKeyResponse(result.APIKey), Key: result.PlainText})
}
