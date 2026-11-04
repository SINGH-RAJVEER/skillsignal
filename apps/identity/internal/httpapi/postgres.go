package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skillsignal/backend/ids"
)

var (
	ErrEmailAlreadyUsed = errors.New("email already used")
	ErrNotFound         = errors.New("identity record not found")
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) RegisterCandidate(ctx context.Context, name, email, passwordHash string) (User, error) {
	return s.register(ctx, name, email, passwordHash, "candidate")
}
func (s *PostgresStore) RegisterEmployer(ctx context.Context, name, email, passwordHash string) (User, error) {
	return s.register(ctx, name, email, passwordHash, "employer")
}
func (s *PostgresStore) register(ctx context.Context, name, email, passwordHash, accountType string) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().UTC()
	user := User{
		ID:          ids.New(),
		Name:        name,
		Email:       email,
		AccountType: accountType,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO "user" (id, name, email, account_type, email_verified, created_at, updated_at)
		 VALUES ($1, $2, $3, $5, false, $4, $4)`,
		user.ID, user.Name, user.Email, now, accountType,
	)
	if err != nil {
		return User{}, registrationError(err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO account (id, account_id, provider_id, user_id, password, created_at, updated_at)
		 VALUES ($1, $2, 'credential', $3, $4, $5, $5)`,
		ids.New(), user.Email, user.ID, passwordHash, now,
	)
	if err != nil {
		return User{}, registrationError(err)
	}
	if accountType == "employer" {
		_, err = tx.Exec(ctx, `INSERT INTO organization_member (id, organization_id, user_id, role)
		 SELECT $1, o.id, $2, o.default_member_role FROM organization o
		 WHERE EXISTS (SELECT 1 FROM organization_email_domain d WHERE d.organization_id=o.id AND d.domain=split_part($3,'@',2))
		 OR EXISTS (SELECT 1 FROM organization_allowed_email e WHERE e.organization_id=o.id AND e.email=$3)
		 ORDER BY o.created_at,o.id LIMIT 1`, ids.New(), user.ID, email)
		if err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) Credentials(ctx context.Context, email string) (User, string, error) {
	var user User
	var passwordHash string
	err := s.pool.QueryRow(ctx,
		`SELECT u.id, u.name, u.email, u.account_type, u.email_verified, u.image, u.is_demo,
		        u.created_at, u.updated_at, a.password
		 FROM "user" u
		 JOIN account a ON a.user_id = u.id AND a.provider_id = 'credential'
		 WHERE u.email = $1`,
		email,
	).Scan(
		&user.ID, &user.Name, &user.Email, &user.AccountType, &user.EmailVerified,
		&user.Image, &user.IsDemo, &user.CreatedAt, &user.UpdatedAt, &passwordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	return user, passwordHash, err
}

func (s *PostgresStore) User(ctx context.Context, userID string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, email, account_type, email_verified, image, is_demo, created_at, updated_at
		 FROM "user" WHERE id = $1`,
		userID,
	).Scan(
		&user.ID, &user.Name, &user.Email, &user.AccountType, &user.EmailVerified,
		&user.Image, &user.IsDemo, &user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func registrationError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrEmailAlreadyUsed
	}
	return err
}
