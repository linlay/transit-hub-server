package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserNotFound     = errors.New("admin user not found")
	ErrUserInactive     = errors.New("admin user inactive")
	ErrInvalidPassword  = errors.New("invalid password")
	ErrSessionNotFound  = errors.New("admin session not found")
	ErrSessionExpired   = errors.New("admin session expired")
	ErrLastActiveUser   = errors.New("cannot disable the last active admin user")
	ErrUsernameRequired = errors.New("username is required")
)

type AdminUser struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type AdminUserWithPassword struct {
	AdminUser
	PasswordHash string
}

type CreateAdminUserParams struct {
	Username string
	Password string
	Status   string
}

type AdminUserPatch struct {
	Username *string
	Password *string
	Status   *string
}

type AdminSession struct {
	ID         string
	UserID     string
	Token      string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
}

func (s *Store) EnsureAdminUser(ctx context.Context, username, password string) (AdminUser, bool, error) {
	username = normalizeUsername(username)
	if username == "" {
		username = "admin"
	}
	user, err := s.GetAdminUserByUsername(ctx, username)
	if err == nil {
		return user.AdminUser, false, nil
	}
	if !errors.Is(err, ErrUserNotFound) {
		return AdminUser{}, false, err
	}
	created, err := s.CreateAdminUser(ctx, CreateAdminUserParams{
		Username: username,
		Password: password,
		Status:   "active",
	})
	return created, true, err
}

func (s *Store) AdminUserCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count)
	return count, err
}

func (s *Store) CreateAdminUser(ctx context.Context, params CreateAdminUserParams) (AdminUser, error) {
	username := normalizeUsername(params.Username)
	if username == "" {
		return AdminUser{}, ErrUsernameRequired
	}
	if strings.TrimSpace(params.Password) == "" {
		return AdminUser{}, errors.New("password is required")
	}
	status := strings.ToLower(strings.TrimSpace(params.Status))
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return AdminUser{}, errors.New("status must be active or disabled")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(params.Password), bcrypt.DefaultCost)
	if err != nil {
		return AdminUser{}, err
	}
	now := time.Now().UTC()
	user := AdminUser{
		ID:        newID("usr"),
		Username:  username,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO admin_users (id, username, password_hash, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, user.ID, user.Username, string(passwordHash), user.Status, formatTime(user.CreatedAt), formatTime(user.UpdatedAt))
	if err != nil {
		return AdminUser{}, err
	}
	return user, nil
}

func (s *Store) ListAdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, status, created_at, updated_at, last_login_at
		FROM admin_users
		ORDER BY username ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []AdminUser{}
	for rows.Next() {
		user, err := scanAdminUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) GetAdminUser(ctx context.Context, id string) (AdminUserWithPassword, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, status, created_at, updated_at, last_login_at
		FROM admin_users
		WHERE id = ?
	`, id)
	return scanAdminUserWithPassword(row)
}

func (s *Store) GetAdminUserByUsername(ctx context.Context, username string) (AdminUserWithPassword, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, status, created_at, updated_at, last_login_at
		FROM admin_users
		WHERE username = ?
	`, normalizeUsername(username))
	return scanAdminUserWithPassword(row)
}

func (s *Store) VerifyAdminLogin(ctx context.Context, username, password string) (AdminUser, error) {
	user, err := s.GetAdminUserByUsername(ctx, username)
	if err != nil {
		return AdminUser{}, err
	}
	if user.Status != "active" {
		return AdminUser{}, ErrUserInactive
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return AdminUser{}, ErrInvalidPassword
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE admin_users
		SET last_login_at = ?, updated_at = ?
		WHERE id = ?
	`, formatTime(now), formatTime(now), user.ID)
	if err != nil {
		return AdminUser{}, err
	}
	user.LastLoginAt = &now
	user.UpdatedAt = now
	return user.AdminUser, nil
}

func (s *Store) UpdateAdminUser(ctx context.Context, id string, patch AdminUserPatch) (AdminUser, error) {
	user, err := s.GetAdminUser(ctx, id)
	if err != nil {
		return AdminUser{}, err
	}
	username := user.Username
	if patch.Username != nil {
		username = normalizeUsername(*patch.Username)
		if username == "" {
			return AdminUser{}, ErrUsernameRequired
		}
	}
	status := user.Status
	if patch.Status != nil {
		status = strings.ToLower(strings.TrimSpace(*patch.Status))
		if status != "active" && status != "disabled" {
			return AdminUser{}, errors.New("status must be active or disabled")
		}
		if user.Status == "active" && status == "disabled" {
			if err := s.ensureNotLastActiveAdmin(ctx, id); err != nil {
				return AdminUser{}, err
			}
		}
	}
	passwordHash := user.PasswordHash
	if patch.Password != nil {
		if strings.TrimSpace(*patch.Password) == "" {
			return AdminUser{}, errors.New("password cannot be empty")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*patch.Password), bcrypt.DefaultCost)
		if err != nil {
			return AdminUser{}, err
		}
		passwordHash = string(hash)
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		UPDATE admin_users
		SET username = ?, password_hash = ?, status = ?, updated_at = ?
		WHERE id = ?
	`, username, passwordHash, status, formatTime(now), id)
	if err != nil {
		return AdminUser{}, err
	}
	return s.GetAdminUserPublic(ctx, id)
}

func (s *Store) GetAdminUserPublic(ctx context.Context, id string) (AdminUser, error) {
	user, err := s.GetAdminUser(ctx, id)
	if err != nil {
		return AdminUser{}, err
	}
	return user.AdminUser, nil
}

func (s *Store) DisableAdminUser(ctx context.Context, id string) (AdminUser, error) {
	status := "disabled"
	return s.UpdateAdminUser(ctx, id, AdminUserPatch{Status: &status})
}

func (s *Store) CreateAdminSession(ctx context.Context, userID string, ttl time.Duration) (AdminSession, error) {
	token, err := newRandomToken()
	if err != nil {
		return AdminSession{}, err
	}
	now := time.Now().UTC()
	session := AdminSession{
		ID:         newID("sess"),
		UserID:     userID,
		Token:      token,
		ExpiresAt:  now.Add(ttl),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO admin_sessions (id, user_id, session_hash, expires_at, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, session.ID, session.UserID, HashKey(token), formatTime(session.ExpiresAt), formatTime(session.CreatedAt), formatTime(session.LastSeenAt))
	if err != nil {
		return AdminSession{}, err
	}
	return session, nil
}

func (s *Store) AuthenticateAdminSession(ctx context.Context, token string, now time.Time) (AdminUser, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.status, u.created_at, u.updated_at, u.last_login_at, s.expires_at
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.user_id
		WHERE s.session_hash = ?
	`, HashKey(token))
	user, expiresAt, err := scanAdminSessionUser(row)
	if err != nil {
		return AdminUser{}, err
	}
	if !expiresAt.After(now) {
		_ = s.DeleteAdminSession(ctx, token)
		return AdminUser{}, ErrSessionExpired
	}
	if user.Status != "active" {
		return AdminUser{}, ErrUserInactive
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE admin_sessions
		SET last_seen_at = ?
		WHERE session_hash = ?
	`, formatTime(now.UTC()), HashKey(token))
	return user, nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE session_hash = ?`, HashKey(token))
	return err
}

func (s *Store) ensureNotLastActiveAdmin(ctx context.Context, id string) error {
	var count int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM admin_users
		WHERE status = 'active' AND id <> ?
	`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrLastActiveUser
	}
	return nil
}

type adminUserScanner interface {
	Scan(dest ...any) error
}

func scanAdminUser(scanner adminUserScanner) (AdminUser, error) {
	var user AdminUser
	var createdAt, updatedAt string
	var lastLoginAt sql.NullString
	err := scanner.Scan(&user.ID, &user.Username, &user.Status, &createdAt, &updatedAt, &lastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrUserNotFound
	}
	if err != nil {
		return AdminUser{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return AdminUser{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return AdminUser{}, err
	}
	user.CreatedAt = parsedCreatedAt
	user.UpdatedAt = parsedUpdatedAt
	if lastLoginAt.Valid {
		parsed, err := parseTime(lastLoginAt.String)
		if err != nil {
			return AdminUser{}, err
		}
		user.LastLoginAt = &parsed
	}
	return user, nil
}

func scanAdminUserWithPassword(scanner adminUserScanner) (AdminUserWithPassword, error) {
	var user AdminUserWithPassword
	var createdAt, updatedAt string
	var lastLoginAt sql.NullString
	err := scanner.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Status, &createdAt, &updatedAt, &lastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUserWithPassword{}, ErrUserNotFound
	}
	if err != nil {
		return AdminUserWithPassword{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return AdminUserWithPassword{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return AdminUserWithPassword{}, err
	}
	user.CreatedAt = parsedCreatedAt
	user.UpdatedAt = parsedUpdatedAt
	if lastLoginAt.Valid {
		parsed, err := parseTime(lastLoginAt.String)
		if err != nil {
			return AdminUserWithPassword{}, err
		}
		user.LastLoginAt = &parsed
	}
	return user, nil
}

func scanAdminSessionUser(scanner adminUserScanner) (AdminUser, time.Time, error) {
	var user AdminUser
	var createdAt, updatedAt, expiresAt string
	var lastLoginAt sql.NullString
	err := scanner.Scan(&user.ID, &user.Username, &user.Status, &createdAt, &updatedAt, &lastLoginAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, time.Time{}, ErrSessionNotFound
	}
	if err != nil {
		return AdminUser{}, time.Time{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return AdminUser{}, time.Time{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return AdminUser{}, time.Time{}, err
	}
	parsedExpiresAt, err := parseTime(expiresAt)
	if err != nil {
		return AdminUser{}, time.Time{}, err
	}
	user.CreatedAt = parsedCreatedAt
	user.UpdatedAt = parsedUpdatedAt
	if lastLoginAt.Valid {
		parsed, err := parseTime(lastLoginAt.String)
		if err != nil {
			return AdminUser{}, time.Time{}, err
		}
		user.LastLoginAt = &parsed
	}
	return user, parsedExpiresAt, nil
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func newRandomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
