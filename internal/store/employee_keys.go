package store

import (
	"context"
	"database/sql"
	"errors"
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
	err := s.db.QueryRowContext(ctx, `SELECT issuer,subject_claim,subject FROM employee_identities WHERE api_key_id=?`, id).Scan(&identity.Issuer, &identity.Claim, &identity.Subject)
	return &identity, err
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
		return s.GetAPIKey(ctx, existing)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, err
	}
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
		policy.Name = identity.Subject
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
	// Disabled/deleted records remain bound; a new token cannot reset entitlement.
	return s.GetAPIKey(ctx, id)
}
