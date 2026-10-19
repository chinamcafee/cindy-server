package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/makecindy/cindy-server/migrations"
)

type DB struct {
	Pool *pgxpool.Pool
	SQL  *sql.DB
}

func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &DB{Pool: pool, SQL: stdlib.OpenDBFromPool(pool)}, nil
}

func (d *DB) Close() {
	if d.SQL != nil {
		_ = d.SQL.Close()
	}
	d.Pool.Close()
}

func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

func Migrate(ctx context.Context, d *DB) error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, d.SQL, "."); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func (d *DB) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.Pool, fn)
}

func (d *DB) Version(ctx context.Context) (int64, error) {
	return goose.GetDBVersionContext(ctx, d.SQL)
}
