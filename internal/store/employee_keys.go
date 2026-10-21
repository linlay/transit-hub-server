package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// EmployeeIdentity is extracted only from verified JWT claims. Claim is part of
// the namespace so changing identity fields cannot silently take over old usage.
type EmployeeIdentity struct {
	Issuer  string `json:"issuer"`
	Claim   string `json:"claim"`
	Subject string `json:"subject"`
}

func (s *Store) employeeIdentity(ctx context.Context, id string) (*EmployeeIdentity, error) {
	var identity EmployeeIdentity
	err := s.db.QueryRowContext(ctx, `SELECT issuer,subject_claim,subject FROM employee_identities WHERE api_key_id=?
 UNION ALL SELECT issuer,subject_claim,subject FROM employee_identity_history WHERE api_key_id=? LIMIT 1`, id, id).Scan(&identity.Issuer, &identity.Claim, &identity.Subject)
	return &identity, err
}

// Archive the old accounting identity and release its active binding atomically
// with deletion. Old logs/usage retain their original key; next use gets a new one.
func archiveEmployeeIdentities(ctx context.Context, tx *sql.Tx, where string, args ...any) error {
	selection := fmt.Sprintf(`SELECT id FROM api_keys WHERE source='employee' AND %s`, where)
	_, err := tx.ExecContext(ctx, `INSERT INTO employee_identity_history(api_key_id,issuer,subject_claim,subject)
 SELECT api_key_id,issuer,subject_claim,subject FROM employee_identities WHERE api_key_id IN (`+selection+`) ON CONFLICT DO NOTHING`, args...)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM employee_identities WHERE api_key_id IN (`+selection+`)`, args...)
	return err
}

// ResolveEmployeeKey creates an accounting record on first authenticated use.
// No API key is generated or returned. Token refresh/device changes reuse it.
func (s *Store) ResolveEmployeeKey(ctx context.Context, identity EmployeeIdentity, policy CreateAPIKeyParams) (APIKey, error) {
	if identity.Issuer == "" || identity.Claim == "" || identity.Subject == "" {
		return APIKey{}, errors.New("missing employee identity")
	}
	// Existing employees use only reads; first-use registration alone takes a write lock.
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT api_key_id FROM employee_identities WHERE issuer=? AND subject_claim=? AND subject=?`, identity.Issuer, identity.Claim, identity.Subject).Scan(&existing)
	if err == nil && existing != "" {
		key, err := s.GetAPIKey(ctx, existing)
		// Backfill legacy identifier-only names without overwriting admin edits,
		// reviving deleted records, or changing entitlement and usage.
		name := strings.TrimSpace(policy.Name)
		if err == nil && name != "" && name != identity.Subject && key.Name == identity.Subject && key.DeletedAt == nil && key.Status == "active" {
			_, err = s.db.ExecContext(ctx, `UPDATE api_keys SET name=? WHERE id=? AND name=? AND deleted_at IS NULL AND status='active'`, name, existing, identity.Subject)
			if err == nil {
				return s.GetAPIKey(ctx, existing)
			}
		}
		return key, err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, err
	}
	defaults, err := s.GetEmployeePolicy(ctx, EmployeePolicy{
		AllowedModels: policy.AllowedModels, RequestQuota: policy.RequestQuota,
		TokenQuota: policy.TokenQuota, QuotaMicrocredits: policy.QuotaMicrocredits, RateLimits: policy.RateLimits,
	})
	if err != nil {
		return APIKey{}, err
	}
	policy.AllowedModels = defaults.AllowedModels
	policy.RequestQuota = defaults.RequestQuota
	policy.TokenQuota = defaults.TokenQuota
	policy.QuotaMicrocredits = defaults.QuotaMicrocredits
	policy.RateLimits = defaults.RateLimits
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, err
	}
	defer tx.Rollback()
	// Acquire the write lock before checking/creating a new identity.
	_, err = tx.ExecContext(ctx, `INSERT INTO employee_identities(issuer,subject_claim,subject) VALUES(?,?,?) ON CONFLICT DO NOTHING`, identity.Issuer, identity.Claim, identity.Subject)
	if err != nil {
		return APIKey{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT api_key_id FROM employee_identities WHERE issuer=? AND subject_claim=? AND subject=?`, identity.Issuer, identity.Claim, identity.Subject).Scan(&id)
	if err != nil {
		return APIKey{}, err
	}
	if id == "" {
		policy.Source = "employee"
		policy.Name = strings.TrimSpace(policy.Name)
		if policy.Name == "" {
			policy.Name = identity.Subject
		}
		created, err := s.createAPIKeyInTx(ctx, tx, policy)
		if err != nil {
			return APIKey{}, err
		}
		id = created.ID
		_, err = tx.ExecContext(ctx, `UPDATE employee_identities SET api_key_id=? WHERE issuer=? AND subject_claim=? AND subject=?`, id, identity.Issuer, identity.Claim, identity.Subject)
		if err != nil {
			return APIKey{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return APIKey{}, err
	}
	// Disabled records remain bound; deletion explicitly releases the identity.
	return s.GetAPIKey(ctx, id)
}
