package pgtest

import (
	"database/sql"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Name возвращает фактическую базу исходного соединения.
func (j *Journal) Name() string {
	var name string
	j.Require(j.db.QueryRowContext(j.Context(), "SELECT current_database()").Scan(&name) == nil, "имя исходной базы")
	return name
}

// Existing открывает новый пул и проверяет имя базы и версию сервера без миграций.
func Existing(t *testing.T, name string, open Opener) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")
	u, err := url.Parse(dsn)
	ok, _ := regexp.MatchString("^momentum_test_[0-9a-f]{24}$", name)
	if !ok || err != nil || !validOptIn(u) || strings.Contains(dsn, "#") || os.Getenv("PQGO_DEBUG") == "1" {
		t.Fatal("неверная локальная тестовая база")
	}
	u.Path, u.RawPath = "/"+name, ""
	db, err := open(t.Context(), u.String())
	if err != nil {
		t.Fatal("ошибка открытия тестовой базы")
	}
	t.Cleanup(func() { db.Close() })
	var actual, version string
	err = db.QueryRowContext(t.Context(), "SELECT current_database(),current_setting('server_version_num')").Scan(&actual, &version)
	if err != nil || actual != name || version != "170011" {
		t.Fatal("не подтверждены база и версия PostgreSQL")
	}
	return db
}

// RemovePaperInput удаляет вход указанного run только в одноразовой fixture.
func (j *Journal) RemovePaperInput(run string) {
	tx, err := j.db.BeginTx(j.Context(), nil)
	j.Require(err == nil, err)
	defer tx.Rollback()
	j.Done(tx.ExecContext(j.Context(), "ALTER TABLE momentum.paper_run_inputs DISABLE TRIGGER paper_run_inputs_immutable"))
	var removed string
	err = tx.QueryRowContext(j.Context(), "DELETE FROM momentum.paper_run_inputs WHERE run_id=$1 RETURNING run_id", run).Scan(&removed)
	j.Require(err == nil && removed == run, "удаление указанного входа", err)
	j.Done(tx.ExecContext(j.Context(), "ALTER TABLE momentum.paper_run_inputs ENABLE TRIGGER paper_run_inputs_immutable"))
	j.Require(tx.Commit() == nil, "фиксация повреждения")
}
