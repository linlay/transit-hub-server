package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// EmployeePolicy supplies defaults only when an authenticated identity has no record.
type EmployeePolicy struct {
	AllowedModels     []string    `json:"allowed_models"`
	RequestQuota      int64       `json:"request_quota"`
	TokenQuota        int64       `json:"token_quota"`
	QuotaMicrocredits int64       `json:"quota_microcredits,string"`
	RateLimits        []RateLimit `json:"rate_limits"`
}

func (s *Store) GetEmployeePolicy(ctx context.Context, fallback EmployeePolicy) (EmployeePolicy, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT policy FROM employee_default_policy WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return EmployeePolicy{}, err
	}
	var policy EmployeePolicy
	err = json.Unmarshal([]byte(raw), &policy)
	return policy, err
}

func (s *Store) SetEmployeePolicy(ctx context.Context, policy EmployeePolicy) error {
	policy.AllowedModels = NormalizeAllowedModels(policy.AllowedModels)
	if len(policy.AllowedModels) == 0 || policy.RequestQuota < 0 || policy.TokenQuota < 0 || policy.QuotaMicrocredits < 0 {
		return errors.New("employee defaults require allowed_models and nonnegative quotas")
	}
	var err error
	policy.RateLimits, err = NormalizeRateLimits(policy.RateLimits)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO employee_default_policy(id,policy) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET policy=excluded.policy`, string(raw))
	return err
}
