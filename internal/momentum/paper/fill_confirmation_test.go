package paper

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
	s "bybit_bot_liquidity/internal/momentum/storage"
)

type fillFixture struct {
	*testing.T
	db    *sql.DB
	owner *s.ExecutionOwner
	in    CycleInput
	p     PreparedCycle
	r     s.PaperFillExpectation
}

// fillDatabase создаёт отдельную базу на явно разрешённом локальном PostgreSQL.
func fillDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MOMENTUM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("нужен одноразовый PostgreSQL")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/momentum_test_") {
		t.Fatal("требуется локальная тестовая база")
	}
	admin, err := s.OpenMomentumPostgres(t.Context(), dsn)
	requireCycleSuccess(t, err)
	defer admin.Close()
	name := "momentum_test_" + strings.ToLower(rand.Text())
	_, err = admin.ExecContext(t.Context(), "CREATE DATABASE "+name)
	requireCycleSuccess(t, err)
	u.Path, u.RawPath = "/"+name, ""
	db, err := s.OpenMomentumPostgres(t.Context(), u.String())
	requireCycleSuccess(t, err)
	t.Cleanup(func() { db.Close() })
	requireCycleSuccess(t, s.MigrateMomentumPostgres(t.Context(), db))
	return db
}

// newFillFixture создаёт область с действующим владельцем, конфигурацией и планом.
func newFillFixture(t *testing.T, corrupt bool) *fillFixture {
	x := &fillFixture{T: t, db: fillDatabase(t), in: cycleFixture(t)}
	x.in.Run.OnboardingID = ""
	scope := s.ControlScope{Environment: "paper", AccountID: x.in.Run.AccountID, StrategyID: x.in.Run.StrategyID}
	var err error
	x.owner, err = s.AcquireExecutionOwner(t.Context(), x.db, s.OwnerIdentity{SessionID: "owner", Host: "test", ProcessID: 1, Environment: "paper", AccountID: scope.AccountID, StrategyID: scope.StrategyID}, time.Now())
	requireCycleSuccess(t, err)
	t.Cleanup(func() { x.owner.Release(context.Background(), time.Now()) })
	x.r.Key.Scope = scope
	_, hash, err := x.in.Run.Config.Snapshot()
	requireCycleSuccess(t, err)
	_, err = s.ApplyPaperCommandState(t.Context(), x.db, s.PaperCommandRequest{Scope: scope, Config: &x.in.Run.Config, ConfigHash: hash, Action: "INITIALIZE", Actor: "SYSTEM", ActorID: "system/test", CommandID: "init", Kind: "initialize", Reason: "genesis"})
	requireCycleSuccess(t, err)
	x.stage(corrupt)
	return x
}

// stage регистрирует run, вход, ревизию и намерения по исходным индексам.
func (x *fillFixture) stage(corrupt bool) {
	x.p = preparedFixture(x.T, x.in)
	x.r.Key.RunID, x.r.Key.InputHash = x.in.Run.RunID, x.p.Hash()
	fills := x.p.ExpectedFills()
	x.r = x.slice(0, model.Zero(), fills[0].Quantity)
	_, err := s.CreateOrReadRebalanceRun(x.Context(), x.db, s.RebalanceRunInput(x.in.Run))
	requireCycleSuccess(x.T, err)
	x.observe(x.r, "", model.Decimal{}, s.ErrPaperFillConfirmationConflict)
	_, err = s.StorePaperInput(x.Context(), x.db, x.p.input)
	requireCycleSuccess(x.T, err)
	x.observe(x.r, "", model.Decimal{}, s.ErrPaperFillConfirmationConflict)
	_, err = s.StorePlanRevision(x.Context(), x.db, x.p.Plan())
	requireCycleSuccess(x.T, err)
	x.observe(x.r, "", model.Decimal{}, nil)
	for index, fill := range fills {
		link, err := s.PaperIntentIdentity(x.p.Hash(), index)
		requireCycleSuccess(x.T, err)
		if corrupt && index == 1 {
			fill = fills[0]
		}
		requireCycleSuccess(x.T, s.RecordOrderIntent(x.Context(), x.db, s.OrderIntent{IntentID: link, OrderLinkID: link, Environment: "paper", AccountID: x.in.Run.AccountID, RunID: x.in.Run.RunID, PlanHash: x.p.Plan().Hash(), PlanRevision: 1, Symbol: fill.Symbol, Side: fill.Side, ReduceOnly: fill.Side == model.FillSell, Quantity: fill.Quantity}))
	}
	x.observe(x.r, "", model.Decimal{}, nil)
}

// slice собирает запрос с точными экономикой и связью исполнения.
func (x *fillFixture) slice(i int, offset, qty model.Decimal) s.PaperFillExpectation {
	r := x.r
	var err error
	r.OrderLinkID, err = s.PaperIntentIdentity(x.p.Hash(), i)
	requireCycleSuccess(x.T, err)
	r.Fill, err = MakeFillSlice(x.p, i, offset, qty)
	requireCycleSuccess(x.T, err)
	return r
}

// facts читает подтверждённые строки независимым снимком.
func (x *fillFixture) facts() s.PaperRunFacts {
	f, err := s.ReadPaperRunFacts(x.Context(), x.db, x.r.Key.Scope, x.r.Key.RunID)
	requireCycleSuccess(x.T, err)
	return f
}

// observe сверяет категорию, признак наличия и точное накопленное количество.
func (x *fillFixture) observe(r s.PaperFillExpectation, state string, q model.Decimal, want error) {
	x.Helper()
	got, found, err := s.ReadPaperFillConfirmation(x.Context(), x.db, r, CheckPaperFill(x.p))
	if !errors.Is(err, want) || found != (state != "") || got.State != state ||
		found && !decimalEqual(got.FilledQuantity, q) || !found && got != (s.PaperFillConfirmation{}) {
		x.Fatalf("исполнение %s: %+v, found=%t, err=%v; ожидались %s, %v", r.Fill.ExecutionID, got, found, err, state, want)
	}
	if want == s.ErrPaperFillConfirmationRejected && !errors.Is(err, ErrPaperProgress) {
		x.Fatal("потеряна категория A2", err)
	}
}

// record сохраняет факты обычным writer.
func (x *fillFixture) record(r s.PaperFillExpectation) error {
	_, err := s.RecordFill(x.Context(), x.db, "paper", x.in.Run.AccountID, r.OrderLinkID, r.Fill)
	return err
}

// TestPaperFillConfirmationEffects проверяет partial, полную сумму одной ссылки и неизменность чтения.
func TestPaperFillConfirmationEffects(t *testing.T) {
	x := newFillFixture(t, false)
	q := cycleAmount(t, "0."+strings.Repeat("0", 140)+"1")
	first := x.slice(0, model.Zero(), q)
	if err := x.record(first); !errors.Is(err, s.ErrExecution) {
		t.Fatal("ожидался отказ ParseQty после COMMIT")
	}
	f := x.facts()
	if len(f.Fills()) != 1 || f.Intents()[first.OrderLinkID].State != "PARTIALLY_FILLED" {
		t.Fatal("нет сохранённого partial")
	}
	x.observe(first, "PARTIALLY_FILLED", q, nil)
	progress, err := ProjectProgress(x.p, []model.ConfirmedFill{first.Fill})
	requireCycleSuccess(t, err)
	want, err := x.p.Cutoff().WalletUSDT.Sub(first.Fill.FeeUSDT)
	requireCycleSuccess(t, err)
	if !decimalEqual(want, progress.ActualAfter().WalletUSDT) {
		t.Fatal("неверное списание BUY")
	}
	rest, err := x.r.Fill.Quantity.Sub(q)
	requireCycleSuccess(t, err)
	if err := x.record(x.slice(0, q, rest)); !errors.Is(err, s.ErrExecution) {
		t.Fatal("ожидался широкий scale полной суммы", err)
	}
	other := x.slice(1, model.Zero(), x.p.ExpectedFills()[1].Quantity)
	requireCycleSuccess(t, x.record(other))
	snapshot := func() string {
		var rows string
		requireCycleSuccess(t, x.db.QueryRowContext(t.Context(), `SELECT
 (SELECT jsonb_agg(i ORDER BY intent_id) FROM momentum.order_intents i)::text ||
 (SELECT jsonb_agg(f ORDER BY execution_id) FROM momentum.fills f)::text ||
 (SELECT jsonb_agg(l ORDER BY source_id,kind) FROM momentum.ledger_entries l)::text`).Scan(&rows))
		return rows
	}
	before, hash := snapshot(), x.facts().DataHash
	for range 2 {
		x.observe(first, "FILLED", x.r.Fill.Quantity, nil)
		x.observe(other, "FILLED", other.Fill.Quantity, nil)
	}
	if before != snapshot() || hash != x.facts().DataHash {
		t.Fatal("чтение изменило факты исполнения или проверенные проводки")
	}
}

// TestPaperFillConfirmationAddress различает отсутствие, конфликт, отказ A2 и недоступность снимка.
func TestPaperFillConfirmationAddress(t *testing.T) {
	x := newFillFixture(t, false)
	r := x.slice(0, model.Zero(), cycleAmount(t, "1"))
	requireCycleSuccess(t, x.record(r))
	for _, edit := range []func(*s.PaperFillExpectation){
		func(v *s.PaperFillExpectation) { v.Fill.Side = model.FillSell },
		func(v *s.PaperFillExpectation) { v.Fill.Symbol = "ETHUSDT" },
		func(v *s.PaperFillExpectation) { v.Fill.Quantity = x.r.Fill.Quantity },
		func(v *s.PaperFillExpectation) { v.Fill.ExecutionPrice = cycleAmount(t, "1") },
		func(v *s.PaperFillExpectation) { v.Fill.FeeUSDT = model.Zero() },
		func(v *s.PaperFillExpectation) { v.Fill.ExecutedAt = v.Fill.ExecutedAt.Add(time.Second) },
		func(v *s.PaperFillExpectation) { v.Key.RunID += "missing" },
		func(v *s.PaperFillExpectation) { v.Key.InputHash = strings.Repeat("a", 64) },
		func(v *s.PaperFillExpectation) { v.OrderLinkID += "other" },
	} {
		v := r
		edit(&v)
		x.observe(v, "", model.Decimal{}, s.ErrPaperFillConfirmationConflict)
	}
	_, err := s.ApplyPaperCommandState(t.Context(), x.db, s.PaperCommandRequest{Scope: r.Key.Scope, ExpectedRevision: 1, ConfigHash: x.p.Binding().ConfigHash, Action: "HALT", Actor: "OPERATOR", ActorID: "operator/test", Kind: "halt", CommandID: "stop", Confirmed: true, Reason: "stop"})
	requireCycleSuccess(t, err)
	receipt := replayedReceipt(t, x.p, []model.ConfirmedFill{r.Fill}, ResolutionDisposition{Kind: "ABORTED", CommandID: "stop", ControlRevision: 2, ResolvedAt: x.in.Run.ScheduledAt})
	_, err = s.CompleteRebalanceRun(t.Context(), x.db, s.RunCompletion{RunID: r.Key.RunID, Outcome: s.RunFailed, PlanHash: x.p.Plan().Hash(), PlanRevision: 1, Reason: string(receipt.Payload())})
	requireCycleSuccess(t, err)
	requireCycleSuccess(t, x.owner.Release(t.Context(), time.Now()))
	next := *x
	next.in = nextCycle(t, x.in, receipt, false)
	next.in.PlanBuilder = singleTarget(cycleAmount(t, "2"))
	next.stage(false)
	neighbor := r
	neighbor.Key = next.r.Key
	next.observe(neighbor, "", model.Decimal{}, s.ErrPaperFillConfirmationConflict)
	neighbor.OrderLinkID = next.r.OrderLinkID
	next.observe(neighbor, "", model.Decimal{}, nil)
	neighbor.Fill.Side = model.FillSell
	next.observe(neighbor, "", model.Decimal{}, s.ErrPaperFillConfirmationConflict)
	next.observe(r, "", model.Decimal{}, s.ErrPaperFillConfirmationRejected)
	x.observe(r, "PARTIALLY_FILLED", r.Fill.Quantity, nil)
	_, err = x.db.ExecContext(t.Context(), "ALTER TABLE momentum.ledger_entries DISABLE TRIGGER ledger_entries_immutable; DELETE FROM momentum.ledger_entries WHERE kind='FEE'")
	requireCycleSuccess(t, err)
	x.observe(r, "", model.Decimal{}, s.ErrPaperFillConfirmationConflict)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, found, err := s.ReadPaperFillConfirmation(ctx, x.db, r, CheckPaperFill(x.p))
	if found || got != (s.PaperFillConfirmation{}) || !errors.Is(err, context.Canceled) || !errors.Is(err, s.ErrPaperFillConfirmationUnavailable) {
		t.Fatal(got, found, err)
	}
	requireCycleSuccess(t, x.db.Close())
	x.observe(r, "", model.Decimal{}, s.ErrPaperFillConfirmationUnavailable)
	r.Fill.ExecutedAt = r.Fill.ExecutedAt.Add(time.Nanosecond)
	x.observe(r, "", model.Decimal{}, s.ErrPaperFillConfirmationArgument)
	got, found, err = s.ReadPaperFillConfirmation(t.Context(), x.db, x.r, nil)
	if found || got != (s.PaperFillConfirmation{}) || !errors.Is(err, s.ErrPaperFillConfirmationArgument) {
		t.Fatal(got, found, err)
	}
}

// TestPaperFillConfirmationPrefix проверяет реальную экономику, порядок и ошибочную связь предшественника.
func TestPaperFillConfirmationPrefix(t *testing.T) {
	for _, kind := range []string{"id", "offset", "order", "price", "fee", "link"} {
		t.Run(kind, func(t *testing.T) {
			x := newFillFixture(t, kind == "link")
			q, err := x.r.Fill.Quantity.Mul(cycleAmount(t, "0.25"))
			requireCycleSuccess(t, err)
			r := x.slice(0, model.Zero(), q)
			switch kind {
			case "id":
				r.Fill.ExecutionID += "bad"
			case "offset":
				r = x.slice(0, q, q)
			case "order":
				r = x.slice(1, model.Zero(), x.p.ExpectedFills()[1].Quantity)
			case "price":
				r.Fill.ExecutionPrice = cycleAmount(t, "1")
			case "fee":
				r.Fill.FeeUSDT = model.Zero()
			case "link":
				r.OrderLinkID, err = s.PaperIntentIdentity(x.p.Hash(), 1)
				requireCycleSuccess(t, err)
				requireCycleSuccess(t, x.record(r))
				r = x.slice(0, q, q)
			}
			requireCycleSuccess(t, x.record(r))
			if kind == "link" {
				fills, err := x.facts().ConfirmedFills()
				requireCycleSuccess(t, err)
				_, err = ProjectProgress(x.p, fills)
				requireCycleSuccess(t, err)
			}
			x.observe(r, "", model.Decimal{}, s.ErrPaperFillConfirmationRejected)
		})
	}
}

// TestPaperFillConfirmationPending читает вторую равную часть до commit и после завершения транзакции.
func TestPaperFillConfirmationPending(t *testing.T) {
	x := newFillFixture(t, false)
	q := cycleAmount(t, "0.01")
	first, second := x.slice(0, model.Zero(), q), x.slice(0, q, q)
	requireCycleSuccess(t, x.record(first))
	for _, commit := range []bool{false, true} {
		tx, err := x.db.BeginTx(t.Context(), nil)
		requireCycleSuccess(t, err)
		defer tx.Rollback()
		_, err = tx.ExecContext(t.Context(), `INSERT INTO momentum.fills
 SELECT environment,account_id,$1,order_link_id,symbol,side,quantity,execution_price,fee_usdt,executed_at
 FROM momentum.fills WHERE execution_id=$2`, second.Fill.ExecutionID, first.Fill.ExecutionID)
		requireCycleSuccess(t, err)
		_, err = tx.ExecContext(t.Context(), `INSERT INTO momentum.ledger_entries
 SELECT environment,account_id,kind,$1,$1,symbol,amount_usdt,occurred_at
 FROM momentum.ledger_entries WHERE execution_id=$2`, second.Fill.ExecutionID, first.Fill.ExecutionID)
		requireCycleSuccess(t, err)
		x.observe(second, "", model.Decimal{}, nil)
		if commit {
			requireCycleSuccess(t, tx.Commit())
			x.observe(second, "PARTIALLY_FILLED", cycleAmount(t, "0.02"), nil)
		} else {
			requireCycleSuccess(t, tx.Rollback())
			x.observe(second, "", model.Decimal{}, nil)
		}
	}
}
