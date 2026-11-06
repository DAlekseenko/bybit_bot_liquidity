package auth

import "testing"

// TestAuthorizeOperations проверяет точные grants, селекторы и обе категории principal.
func TestAuthorizeOperations(t *testing.T) {
	scope := Scope{"strat", EnvironmentPaper, stringPtr("run"), stringPtr("acct")}
	operations := []struct {
		name string
		role Role
		kind ScopeType
	}{
		{"get_status", RoleRead, ScopeEnvironment},
		{"get_market_state", RoleRead, ScopeEnvironment},
		{"get_portfolio", RoleRead, ScopeAccount},
		{"get_positions", RoleRead, ScopeAccount},
		{"get_recent_orders", RoleRead, ScopeAccount},
		{"get_performance", RoleRead, ScopeRun},
		{"get_recent_signals", RoleRead, ScopeRun},
		{"get_strategy_config", RoleRead, ScopeStrategy},
		{"pause_trading", RoleOperate, ScopeEnvironment},
	}
	for _, human := range []bool{false, true} {
		for _, operation := range operations {
			for _, role := range []Role{RoleRead, RoleOperate, RoleAdmin} {
				for _, kind := range []ScopeType{ScopeStrategy, ScopeEnvironment, ScopeRun, ScopeAccount} {
					want := ErrForbidden
					if role == operation.role && kind == operation.kind && (operation.name != "pause_trading" || human) {
						want = nil
					}
					checkAuthorization(t, policyPrincipal(t, human, Grant{role, kind, scope}), operation.name, scope, want)
				}
			}
			missing := scope
			switch operation.kind {
			case ScopeRun:
				missing.RunID = nil
			case ScopeAccount:
				missing.AccountRef = nil
			default:
				continue
			}
			checkAuthorization(t, policyPrincipal(t, human), operation.name, missing, ErrInvalidArgument)
		}
		p := policyPrincipal(t, human, Grant{RoleRead, ScopeRun, scope}, Grant{RoleOperate, ScopeEnvironment, scope}, Grant{RoleAdmin, ScopeEnvironment, scope})
		if !human {
			checkAuthorization(t, p, "pause_trading", scope, ErrForbidden)
		}
		for _, op := range []string{"prepare_resume", "prepare_config_change", "confirm_action", "cancel_action", "run_backtest", "get_backtest_status", "get_backtest_result"} {
			checkAuthorization(t, p, op, scope, ErrForbidden)
			if op == "run_backtest" || op == "get_backtest_status" || op == "get_backtest_result" {
				missing := scope
				missing.RunID = nil
				checkAuthorization(t, p, op, missing, ErrInvalidArgument)
			}
		}
		for _, op := range []string{"resume_trading", "update_config", "start_backtest", "cancel_backtest", "get_backtest_artifact", "unknown"} {
			checkAuthorization(t, p, op, Scope{}, ErrUnsupported)
		}
	}
}

// TestAuthorizeTuples проверяет каждый компонент, оба risk grants и независимость результата.
func TestAuthorizeTuples(t *testing.T) {
	scope := Scope{"strat", EnvironmentPaper, stringPtr("run"), stringPtr("acct")}
	variants := []Scope{
		{"other", EnvironmentPaper, scope.RunID, scope.AccountRef},
		{"strat", EnvironmentTestnet, scope.RunID, scope.AccountRef},
		{"strat", EnvironmentPaper, stringPtr("other"), scope.AccountRef},
		{"strat", EnvironmentPaper, scope.RunID, stringPtr("other")},
		{"strat", EnvironmentPaper, nil, scope.AccountRef},
		{"strat", EnvironmentPaper, scope.RunID, nil},
	}
	for _, human := range []bool{false, true} {
		strategy, account := Grant{RoleRead, ScopeStrategy, scope}, Grant{RoleRead, ScopeAccount, scope}
		for _, grants := range [][]Grant{nil, {strategy}, {account}} {
			checkAuthorization(t, policyPrincipal(t, human, grants...), "get_risk_status", scope, ErrForbidden)
		}
		for _, role := range []Role{RoleOperate, RoleAdmin} {
			for _, grants := range [][]Grant{{{role, ScopeStrategy, scope}, account}, {strategy, {role, ScopeAccount, scope}}} {
				checkAuthorization(t, policyPrincipal(t, human, grants...), "get_risk_status", scope, ErrForbidden)
			}
		}
		for _, variant := range variants {
			checkAuthorization(t, policyPrincipal(t, human, strategy), "get_strategy_config", variant, ErrForbidden)
			checkAuthorization(t, policyPrincipal(t, human, Grant{RoleRead, ScopeStrategy, variant}), "get_strategy_config", scope, ErrForbidden)
			if human {
				checkAuthorization(t, policyPrincipal(t, human, Grant{RoleOperate, ScopeEnvironment, scope}), "pause_trading", variant, ErrForbidden)
				checkAuthorization(t, policyPrincipal(t, human, Grant{RoleOperate, ScopeEnvironment, variant}), "pause_trading", scope, ErrForbidden)
			}
			wrongStrategy, wrongAccount := strategy, account
			wrongStrategy.Scope, wrongAccount.Scope = variant, variant
			for _, grants := range [][]Grant{{wrongStrategy, account}, {strategy, wrongAccount}} {
				checkAuthorization(t, policyPrincipal(t, human, grants...), "get_risk_status", scope, ErrForbidden)
			}
		}
		p := policyPrincipal(t, human, strategy, account)
		missing := scope
		missing.AccountRef = nil
		checkAuthorization(t, p, "get_risk_status", missing, ErrInvalidArgument)
		got := checkAuthorization(t, p, "get_risk_status", scope, nil)
		*got.RunID, *got.AccountRef = "changed", "changed"
		checkAuthorization(t, p, "get_risk_status", scope, nil)
	}
}

// TestAuthorizePrecedence проверяет wiring валидатора и порядок отказов с нулевым Scope.
func TestAuthorizePrecedence(t *testing.T) {
	checkAuthorization(t, Principal{}, "unknown", Scope{}, ErrUnauthenticated)
	p := policyPrincipal(t, false)
	for _, scope := range []Scope{{}, {"strat", "LIVE", nil, nil}, {"strat", EnvironmentPaper, stringPtr(""), nil}, {"strat", EnvironmentPaper, nil, stringPtr("")}} {
		want := ErrInvalidArgument
		if scope.Environment == "LIVE" {
			want = ErrUnsupported
		}
		checkAuthorization(t, p, "run_backtest", scope, want)
		checkAuthorization(t, p, "pause_trading", scope, want)
	}
}

// policyPrincipal создаёт principal через конструктор; private human proof существует только в тесте.
func policyPrincipal(t *testing.T, human bool, grants ...Grant) Principal {
	t.Helper()
	var p Principal
	var err error
	if human {
		identity := HumanIdentity{actor: Actor{"test", "user", "room"}, verified: true}
		p, err = NewHumanPrincipal(identity, "req", grants)
	} else {
		p, err = NewServicePrincipal("svc", "req", grants)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// checkAuthorization сверяет точную ошибку, полный результат и нулевой Scope при отказе.
func checkAuthorization(t *testing.T, p Principal, op string, scope Scope, want error) Scope {
	t.Helper()
	got, err := p.Authorize(op, scope)
	expected := Scope{}
	if want == nil {
		expected = scope
	}
	if err != want || !equalScope(got, expected) {
		t.Fatalf("kind=%s op=%s input=%+v got=%+v err=%v want=%v", p.Kind(), op, scope, got, err, want)
	}
	return got
}
