package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type taskDatabase interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Begin(context.Context) (pgx.Tx, error)
}
type taskLockKey struct{}
type lockedTask struct {
	id int64
	db taskDatabase
}

func (r *TaskRepo) dbFor(ctx context.Context) taskDatabase {
	if held, ok := ctx.Value(taskLockKey{}).(lockedTask); ok {
		return held.db
	}
	return r.db
}
func taskLocked(ctx context.Context, id int64) bool {
	held, ok := ctx.Value(taskLockKey{}).(lockedTask)
	return ok && held.id == id
}

// Pin all repository calls to the lock-owning connection. Otherwise concurrent
// workers can exhaust the pool while holding locks and waiting for another conn.
func (r *TaskRepo) WithTaskLock(ctx context.Context, id int64, work func(context.Context) error) error {
	if taskLocked(ctx, id) {
		return work(ctx)
	}
	if _, ok := ctx.Value(taskLockKey{}).(lockedTask); ok {
		return fmt.Errorf("nested task lock for another task")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, -id); err != nil {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = conn.Conn().Close(cleanup)
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := conn.Exec(cleanup, `select pg_advisory_unlock($1)`, -id); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	return work(context.WithValue(ctx, taskLockKey{}, lockedTask{id: id, db: conn}))
}
