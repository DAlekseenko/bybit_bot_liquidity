package pgtest

import "context"

// Deadlocks читает подтверждённые сервером жертвы в этой одноразовой БД.
func (j *Journal) Deadlocks(ctx context.Context, minimum int) int {
	for {
		var n int
		err := j.db.QueryRowContext(ctx, "SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()").Scan(&n)
		j.Require(err == nil, err)
		if n >= minimum {
			return n
		}
	}
}
