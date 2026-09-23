package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/linlay/transit-hub/internal/accesskey"
)

var ErrDeviceKeyUnavailable = errors.New("bound api key is disabled, deleted or expired")

// BindAPIKey acquires a SQLite write lock before reading the unique binding.
// The binding is retained after deletion so re-binding cannot reset entitlement.
func (s *Store) BindAPIKey(ctx context.Context, svc *accesskey.Service, identity accesskey.Identity, device, name string, now time.Time) (CreatedAPIKey, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreatedAPIKey{}, false, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO device_key_bindings (auth_issuer,auth_subject,device_id) VALUES (?,?,?) ON CONFLICT DO NOTHING`, identity.Issuer, identity.Subject, device)
	if err != nil {
		return CreatedAPIKey{}, false, err
	}
	var keyID, encryptionID string
	var ciphertext []byte
	err = tx.QueryRowContext(ctx, `SELECT api_key_id,encryption_key_id,key_ciphertext FROM device_key_bindings WHERE auth_issuer=? AND auth_subject=? AND device_id=?`, identity.Issuer, identity.Subject, device).Scan(&keyID, &encryptionID, &ciphertext)
	if err != nil {
		return CreatedAPIKey{}, false, err
	}
	created := keyID == ""
	var result CreatedAPIKey
	if created {
		expiry := now.Add(svc.TTL)
		result, err = s.createAPIKeyInTx(ctx, tx, CreateAPIKeyParams{Name: name, Prefix: "dk", Source: "access_token", ExpiresAt: &expiry, AllowedModels: svc.Config.AllowedModels, RequestQuota: svc.Config.RequestQuota, TokenQuota: svc.Config.TokenQuota, CostQuotaMicro: svc.Config.CostQuotaMicro, RateLimits: deviceRateLimits(svc.Config.RateLimits)})
		if err != nil {
			return CreatedAPIKey{}, false, err
		}
		keyID = result.ID
	} else {
		var status string
		var expired bool
		var expiresAt sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT status, forced_expired OR deleted_at IS NOT NULL, expires_at FROM api_keys WHERE id=?`, keyID).Scan(&status, &expired, &expiresAt)
		if err != nil {
			return CreatedAPIKey{}, false, err
		}
		if expiresAt.Valid {
			deadline, parseErr := parseTime(expiresAt.String)
			if parseErr != nil {
				return CreatedAPIKey{}, false, parseErr
			}
			expired = expired || !now.Before(deadline)
		}
		if status != "active" || expired {
			return CreatedAPIKey{}, false, ErrDeviceKeyUnavailable
		}
	}
	aad, _ := json.Marshal([]string{identity.Issuer, identity.Subject, device, keyID})
	if created {
		encryptionID, ciphertext, err = svc.Seal(result.PlainText, aad)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE device_key_bindings SET api_key_id=?,encryption_key_id=?,key_ciphertext=? WHERE auth_issuer=? AND auth_subject=? AND device_id=?`, keyID, encryptionID, ciphertext, identity.Issuer, identity.Subject, device)
		}
	} else {
		result.PlainText, err = svc.Open(encryptionID, ciphertext, aad)
	}
	if err != nil {
		return CreatedAPIKey{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return CreatedAPIKey{}, false, err
	}
	result.APIKey, err = s.GetAPIKey(ctx, keyID)
	return result, created, err
}

func deviceRateLimits(limits []accesskey.RateLimit) []RateLimit {
	result := make([]RateLimit, 0, len(limits))
	for _, l := range limits {
		result = append(result, RateLimit{Window: l.Window, RequestQuota: l.RequestQuota, TokenQuota: l.TokenQuota, CostQuotaMicro: l.CostQuotaMicro})
	}
	return result
}
