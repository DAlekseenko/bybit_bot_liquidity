package storage

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
)

const (
	controlInsert = `INSERT INTO momentum.control_revisions(environment,account_id,strategy_id,revision,mode,action,
 actor,reason,config_hash,confirmation_id,reconciliation_id) VALUES `
	controlScope = `'testnet','acc','momentum-v1'`
	cfgRef       = `sha256('cfg'::bytea)`
)

// controlDatabase создаёт схему и снимок конфигурации для FK.
func controlDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestPostgres(t)
	migrationResult(t, MigrateMomentumPostgres(context.Background(), db), nil)
	marketStatement(t, db, "INSERT INTO momentum.config_snapshots(config_hash,payload) VALUES("+cfgRef+",'cfg'::bytea)", true)
	return db
}

// controlRow собирает строку ревизии управления для указанной области.
func controlRow(rev int, mode, action, actor, confirmation, reconciliation string) string {
	return "(" + controlScope + "," + strconv.Itoa(rev) + ",'" + mode + "','" + action + "','" + actor + "','r'," +
		cfgRef + "," + confirmation + "," + reconciliation + ")"
}

// TestControlRevisionChain проверяет цепочку, сериализуемость ревизий и выход из halt только через RESUME.
func TestControlRevisionChain(t *testing.T) {
	db := controlDatabase(t)
	runSchemaSQLState(t, db, "23514", controlInsert+controlRow(1, "PAUSED_ENTRIES", "PAUSE_ENTRIES", "OPERATOR", "NULL", "NULL"))
	marketStatement(t, db, controlInsert+controlRow(1, "ACTIVE", "INITIALIZE", "SYSTEM", "NULL", "NULL"), true)
	marketStatement(t, db, controlInsert+controlRow(2, "PAUSED_ENTRIES", "PAUSE_ENTRIES", "OPERATOR", "NULL", "NULL"), true)
	runSchemaSQLState(t, db, "23505", controlInsert+controlRow(2, "HALTED", "HALT", "OPERATOR", "NULL", "NULL"))
	runSchemaSQLState(t, db, "23503", controlInsert+controlRow(4, "HALTED", "HALT", "OPERATOR", "NULL", "NULL"))
	runSchemaSQLState(t, db, "23514", controlInsert+controlRow(3, "ACTIVE", "HALT", "OPERATOR", "NULL", "NULL"))
	marketStatement(t, db, controlInsert+controlRow(3, "HALTED", "HALT", "RISK_CONTROLLER", "NULL", "NULL"), true)
	for _, row := range []string{
		controlRow(4, "PAUSED_ENTRIES", "PAUSE_ENTRIES", "OPERATOR", "NULL", "NULL"),
		controlRow(4, "ACTIVE", "RESUME", "RISK_CONTROLLER", "'c'", "'rec'"),
		controlRow(4, "ACTIVE", "RESUME", "OPERATOR", "'c'", "NULL"),
		controlRow(4, "ACTIVE", "RESUME", "OPERATOR", "NULL", "'rec'"),
		controlRow(4, "ACTIVE", "INITIALIZE", "SYSTEM", "NULL", "NULL"),
	} {
		runSchemaSQLState(t, db, "23514", controlInsert+row)
	}
	const latest = "SELECT mode FROM momentum.control_revisions ORDER BY revision DESC LIMIT 1"
	migrationValue(t, db, latest, "HALTED")
	marketStatement(t, db, controlInsert+controlRow(4, "ACTIVE", "RESUME", "OPERATOR", "'confirm-1'", "'rec-1'"), true)
	migrationValue(t, db, latest, "ACTIVE")
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.control_revisions SET mode='ACTIVE' WHERE revision=3")
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.control_revisions WHERE revision=4")
}

// TestControlRevisionBatchInsert проверяет, что пакетная вставка не обходит запрет выхода из halt.
func TestControlRevisionBatchInsert(t *testing.T) {
	db := controlDatabase(t)
	marketStatement(t, db, controlInsert+controlRow(1, "ACTIVE", "INITIALIZE", "SYSTEM", "NULL", "NULL"), true)
	pause, halt := controlRow(3, "PAUSED_ENTRIES", "PAUSE_ENTRIES", "OPERATOR", "NULL", "NULL"),
		controlRow(2, "HALTED", "HALT", "RISK_CONTROLLER", "NULL", "NULL")
	runSchemaSQLState(t, db, "23503", controlInsert+pause+","+halt)
	runSchemaSQLState(t, db, "23514", controlInsert+halt+","+pause)
	migrationValue(t, db, "SELECT count(*) FROM momentum.control_revisions", "1")
}

// TestEquitySnapshotSchema проверяет тождество equity, ключ и неизменность.
func TestEquitySnapshotSchema(t *testing.T) {
	db := controlDatabase(t)
	const insert = `INSERT INTO momentum.equity_snapshots(environment,account_id,strategy_id,observed_at,source,
 wallet_usdt,unrealized_pnl_usdt,equity_usdt,data_fresh) VALUES `
	marketStatement(t, db, insert+"("+controlScope+",'2026-01-05+00','REST',9000,-12.5,8987.5,true)", true)
	runSchemaSQLState(t, db, "23514", insert+"("+controlScope+",'2026-01-06+00','REST',9000,-12.5,9000,true)")
	runSchemaSQLState(t, db, "23514", insert+"("+controlScope+",'2026-01-06+00','GUESS',1,0,1,true)")
	runSchemaSQLState(t, db, "23505", insert+"("+controlScope+",'2026-01-05+00','REST',1,0,1,false)")
	marketStatement(t, db, insert+"("+controlScope+",'2026-01-05+00','WEBSOCKET',1,0,1,false)", true)
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.equity_snapshots SET data_fresh=true")
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.equity_snapshots")
}

// TestRiskEventSchema проверяет аудит порога, config hash и связь со стоп-ревизией.
func TestRiskEventSchema(t *testing.T) {
	db := controlDatabase(t)
	marketStatement(t, db, controlInsert+controlRow(1, "ACTIVE", "INITIALIZE", "SYSTEM", "NULL", "NULL"), true)
	marketStatement(t, db, controlInsert+controlRow(2, "HALTED", "HALT", "RISK_CONTROLLER", "NULL", "NULL"), true)
	const insert = `INSERT INTO momentum.risk_events(event_id,environment,account_id,strategy_id,kind,threshold_usdt,
 equity_usdt,config_hash,control_revision,detail,occurred_at) VALUES `
	marketStatement(t, db, insert+"('e1',"+controlScope+",'EQUITY_STOP',8000,7999.5,"+cfgRef+",2,'stop','2026-01-05+00')", true)
	marketStatement(t, db, insert+"('e2',"+controlScope+",'STALE_DATA',NULL,NULL,"+cfgRef+",NULL,'mark stale','2026-01-05+00')", true)
	for _, tc := range []struct{ code, values string }{
		{"23514", "('e3'," + controlScope + ",'EQUITY_STOP',8000,8000.1," + cfgRef + ",2,'stop','2026-01-05+00')"},
		{"23514", "('e3'," + controlScope + ",'EQUITY_STOP',NULL,7000," + cfgRef + ",2,'stop','2026-01-05+00')"},
		{"23514", "('e3'," + controlScope + ",'EQUITY_STOP',8000,7000," + cfgRef + ",NULL,'stop','2026-01-05+00')"},
		{"23503", "('e3'," + controlScope + ",'EQUITY_STOP',8000,7000," + cfgRef + ",9,'stop','2026-01-05+00')"},
		{"23503", "('e3'," + controlScope + ",'STALE_DATA',NULL,NULL,sha256('x'::bytea),NULL,'d','2026-01-05+00')"},
		{"23514", "('e3'," + controlScope + ",'GUESS',NULL,NULL," + cfgRef + ",NULL,'d','2026-01-05+00')"},
	} {
		runSchemaSQLState(t, db, tc.code, insert+tc.values)
	}
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.risk_events SET detail='x' WHERE event_id='e1'")
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.risk_events WHERE event_id='e2'")
}

// TestExecutionOwnerSessions проверяет одного активного владельца и сохранение истории stale owner.
func TestExecutionOwnerSessions(t *testing.T) {
	db := controlDatabase(t)
	const insert = `INSERT INTO momentum.execution_owner_sessions(session_id,environment,account_id,strategy_id,host,
 process_id,started_at,last_seen_at) VALUES `
	marketStatement(t, db, insert+"('s1',"+controlScope+",'host-a',101,'2026-01-05+00','2026-01-05+00')", true)
	runSchemaSQLState(t, db, "23505", insert+"('s2',"+controlScope+",'host-b',202,'2026-01-05+00','2026-01-05+00')")
	marketStatement(t, db, insert+"('s3','testnet','acc','other-strategy','host-b',202,'2026-01-05+00','2026-01-05+00')", true)
	marketStatement(t, db, "UPDATE momentum.execution_owner_sessions SET last_seen_at='2026-01-05 01:00+00' WHERE session_id='s1'", true)
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.execution_owner_sessions SET last_seen_at='2026-01-05+00' WHERE session_id='s1'")
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.execution_owner_sessions SET host='host-z' WHERE session_id='s1'")
	runSchemaSQLState(t, db, "23514", "UPDATE momentum.execution_owner_sessions SET ended_at='2026-01-05 02:00+00' WHERE session_id='s1'")
	marketStatement(t, db, `UPDATE momentum.execution_owner_sessions SET ended_at='2026-01-05 02:00+00',
 end_reason='STALE_TAKEOVER' WHERE session_id='s1'`, true)
	marketStatement(t, db, insert+"('s2',"+controlScope+",'host-b',202,'2026-01-05 03:00+00','2026-01-05 03:00+00')", true)
	runSchemaSQLState(t, db, "55000", "UPDATE momentum.execution_owner_sessions SET end_reason='CLEAN_SHUTDOWN' WHERE session_id='s1'")
	runSchemaSQLState(t, db, "55000", "DELETE FROM momentum.execution_owner_sessions WHERE session_id='s1'")
	migrationValue(t, db, "SELECT string_agg(session_id||':'||coalesce(end_reason,'active'),',' ORDER BY session_id) "+
		"FROM momentum.execution_owner_sessions WHERE strategy_id='momentum-v1'", "s1:STALE_TAKEOVER,s2:active")
}
