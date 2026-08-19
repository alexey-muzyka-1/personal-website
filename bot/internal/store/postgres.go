package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

var (
	_ funnel.DB    = (*Postgres)(nil)
	_ funnel.Store = (*pgStore)(nil)
)

// Postgres — боевое хранилище воронки.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres открывает пул и сразу проверяет соединение: сломанный DSN
// должен ронять старт процесса, а не первого пришедшего человека.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing database dsn: %w", err)
	}
	// Нагрузка — единицы запросов в секунду. Пул маленький, чтобы бот не
	// съедал лимит соединений managed-базы.
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() {
	p.pool.Close()
}

// Atomically выполняет единицу работы в одной транзакции.
func (p *Postgres) Atomically(ctx context.Context, fn func(funnel.Store) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	// Rollback после успешного Commit возвращает ErrTxClosed и ничего не
	// делает — поэтому defer здесь безопасен.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(&pgStore{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// pgStore — операции внутри транзакции.
type pgStore struct {
	tx pgx.Tx
}

func (s *pgStore) MarkUpdate(ctx context.Context, updateID int64) (bool, error) {
	const query = `
		insert into processed_updates (update_id, processed_at)
		values ($1, now())
		on conflict (update_id) do nothing`

	tag, err := s.tx.Exec(ctx, query, updateID)
	if err != nil {
		return false, fmt.Errorf("marking update: %w", err)
	}
	// Ноль вставленных строк = такой update уже обработан.
	return tag.RowsAffected() == 0, nil
}

func (s *pgStore) SaveUser(ctx context.Context, u funnel.User, at time.Time) error {
	const query = `
		insert into users (telegram_id, username, first_name, first_seen_at, last_seen_at)
		values ($1, $2, $3, $4, $4)
		on conflict (telegram_id) do update
		set username     = excluded.username,
		    first_name   = excluded.first_name,
		    last_seen_at = excluded.last_seen_at`

	if _, err := s.tx.Exec(ctx, query, u.TelegramID, u.Username, u.FirstName, at); err != nil {
		return fmt.Errorf("saving user: %w", err)
	}
	return nil
}

func (s *pgStore) AppendAttribution(ctx context.Context, a funnel.Attribution) error {
	const query = `
		insert into attributions (telegram_id, source_id, raw_payload, occurred_at)
		values ($1, $2, $3, $4)`

	if _, err := s.tx.Exec(ctx, query, a.TelegramID, a.SourceID, a.RawPayload, a.OccurredAt); err != nil {
		return fmt.Errorf("appending attribution: %w", err)
	}
	return nil
}

func (s *pgStore) LastSource(ctx context.Context, telegramID int64) (string, error) {
	const query = `
		select source_id
		from attributions
		where telegram_id = $1 and source_id <> ''
		order by occurred_at desc, id desc
		limit 1`

	var sourceID string
	err := s.tx.QueryRow(ctx, query, telegramID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Человек без источника — обычное дело, а не сбой.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading last source: %w", err)
	}
	return sourceID, nil
}

func (s *pgStore) AppendEvent(ctx context.Context, e funnel.Event) error {
	const query = `
		insert into events (telegram_id, name, source_id, material_id, metadata, occurred_at)
		values ($1, $2, $3, $4, $5, $6)`

	metadata, err := json.Marshal(orEmpty(e.Metadata))
	if err != nil {
		return fmt.Errorf("encoding event metadata: %w", err)
	}

	_, err = s.tx.Exec(ctx, query, e.TelegramID, e.Name, e.SourceID, e.MaterialID, metadata, e.OccurredAt)
	if err != nil {
		return fmt.Errorf("appending event %s: %w", e.Name, err)
	}
	return nil
}

func (s *pgStore) SaveLink(ctx context.Context, l funnel.Link) error {
	const query = `
		insert into links (token, telegram_id, material_id, source_id, created_at)
		values ($1, $2, $3, $4, $5)`

	if _, err := s.tx.Exec(ctx, query, l.Token, l.TelegramID, l.MaterialID, l.SourceID, l.CreatedAt); err != nil {
		return fmt.Errorf("saving link: %w", err)
	}
	return nil
}

func (s *pgStore) LinkByToken(ctx context.Context, token string) (funnel.Link, bool, error) {
	const query = `
		select token, telegram_id, material_id, source_id, created_at
		from links
		where token = $1`

	var l funnel.Link
	err := s.tx.QueryRow(ctx, query, token).
		Scan(&l.Token, &l.TelegramID, &l.MaterialID, &l.SourceID, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return funnel.Link{}, false, nil
	}
	if err != nil {
		return funnel.Link{}, false, fmt.Errorf("reading link: %w", err)
	}
	return l, true, nil
}

// orEmpty: nil-карта в jsonb должна стать {}, а не null.
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
