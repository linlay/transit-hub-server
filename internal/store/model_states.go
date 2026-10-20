package store

import "context"

func (s *Store) ModelDisabled(ctx context.Context, protocol, model string) (bool, error) {
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM model_states WHERE protocol = ? AND public_model = ? AND disabled = 1)`, protocol, model).Scan(&disabled)
	return disabled, err
}

func (s *Store) SetModelEnabled(ctx context.Context, protocol, model string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_states (protocol, public_model, disabled) VALUES (?, ?, ?) ON CONFLICT(protocol, public_model) DO UPDATE SET disabled = excluded.disabled`, protocol, model, !enabled)
	return err
}
