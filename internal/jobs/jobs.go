// Package jobs runs queued jobs out of the Postgres `jobs` table using
// FOR UPDATE SKIP LOCKED, matching upstream's database queue driver.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxAttempts mirrors Laravel's default tries before a job dead-letters.
const MaxAttempts = 3

// Handler processes one claimed job payload.
type Handler func(ctx context.Context, data json.RawMessage) error

// envelope is the tock job payload stored in jobs.payload. Rows restored
// from upstream dumps contain PHP-serialized commands and fail this parse —
// they dead-letter after MaxAttempts like any unprocessable job.
type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Worker polls the jobs table for pending jobs.
type Worker struct {
	pool     *pgxpool.Pool
	interval time.Duration
	logger   *slog.Logger
	handlers map[string]Handler
}

// New returns a Worker polling every second, matching Laravel's queue cadence.
func New(pool *pgxpool.Pool) *Worker {
	return &Worker{
		pool:     pool,
		interval: time.Second,
		logger:   slog.Default(),
		handlers: map[string]Handler{},
	}
}

// Handle registers a job type handler.
func (w *Worker) Handle(jobType string, h Handler) {
	w.handlers[jobType] = h
}

// Enqueue inserts a job row shaped like Laravel's database queue driver.
func Enqueue(ctx context.Context, pool *pgxpool.Pool, queue, jobType string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(envelope{Type: jobType, Data: raw})
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = pool.Exec(ctx,
		`INSERT INTO jobs (queue, payload, attempts, reserved_at, available_at, created_at)
		 VALUES ($1, $2, 0, NULL, $3, $3)`, queue, string(payload), now)
	return err
}

// Run polls until ctx is cancelled, claiming and executing jobs one at a time.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for {
				more, err := w.tick(ctx)
				if err != nil {
					w.logger.Warn("job tick failed", "error", err)
				}
				if !more {
					break
				}
			}
		}
	}
}

// tick claims at most one job and executes it. Returns false when the
// queue is empty.
func (w *Worker) tick(ctx context.Context) (bool, error) {
	now := time.Now().Unix()
	var id int64
	var queue, payload string
	var attempts int
	err := w.pool.QueryRow(ctx, `
		UPDATE jobs SET reserved_at = $1, attempts = attempts + 1
		WHERE id = (
			SELECT id FROM jobs
			WHERE (reserved_at IS NULL OR reserved_at < $1 - 90)
			  AND available_at <= $1
			ORDER BY id LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, queue, payload, attempts`, now).Scan(&id, &queue, &payload, &attempts)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	jobErr := w.execute(ctx, payload)
	if jobErr == nil {
		if _, err := w.pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id); err != nil {
			return true, err
		}
		return true, nil
	}

	w.logger.Warn("job failed", "id", id, "attempts", attempts, "error", jobErr)
	if attempts >= MaxAttempts {
		w.deadLetter(ctx, id, queue, payload, jobErr)
		return true, nil
	}
	releaseAt := now + int64(10*attempts)
	_, err = w.pool.Exec(ctx,
		`UPDATE jobs SET reserved_at = NULL, available_at = $2 WHERE id = $1`, id, releaseAt)
	return true, err
}

func (w *Worker) execute(ctx context.Context, payload string) error {
	var env envelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return fmt.Errorf("unparseable payload: %w", err)
	}
	h, ok := w.handlers[env.Type]
	if !ok {
		return fmt.Errorf("no handler for job type %q", env.Type)
	}
	return h(ctx, env.Data)
}

func (w *Worker) deadLetter(ctx context.Context, id int64, queue, payload string, jobErr error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _ = tx.Exec(ctx,
		`INSERT INTO failed_jobs (uuid, connection, queue, payload, exception, failed_at)
		 VALUES ($1, 'database', $2, $3, $4, now())`,
		uuid.NewString(), queue, payload, jobErr.Error())
	_, _ = tx.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id)
	_ = tx.Commit(ctx)
}
