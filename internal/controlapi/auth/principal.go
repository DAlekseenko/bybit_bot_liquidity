package auth

import "errors"

var (
	// ErrForbidden сообщает об отклонённой операции.
	ErrForbidden = errors.New("forbidden")
)

type operationGrant struct {
	role      Role
	scopeType ScopeType
}

// Authorize проверяет политику операции и возвращает копию совпавшей выдачи.
func (p Principal) Authorize(operation string, scope Scope) (Scope, error) {
	if !p.valid {
		return Scope{}, ErrUnauthenticated
	}
	required, enabled, known := authorizePolicy(operation)
	if !known {
		return Scope{}, ErrUnsupported
	}
	if err := validateScope(scope); err != nil {
		return Scope{}, err
	}
	if needsRun(operation) && scope.RunID == nil || needsAccount(operation) && scope.AccountRef == nil {
		return Scope{}, ErrInvalidArgument
	}
	if !enabled {
		return Scope{}, ErrForbidden
	}
	if operation == "pause_trading" && p.kind != PrincipalHuman {
		return Scope{}, ErrForbidden
	}
	var matched Scope
	for _, need := range required {
		found := false
		for _, grant := range p.grants {
			if grant.Role == need.role && grant.ScopeType == need.scopeType && equalScope(grant.Scope, scope) {
				matched, found = grant.Scope, true
				break
			}
		}
		if !found {
			return Scope{}, ErrForbidden
		}
	}
	return cloneScope(matched), nil
}

// authorizePolicy задаёт закрытое соответствие операции и grants.
func authorizePolicy(operation string) ([]operationGrant, bool, bool) {
	switch operation {
	case "get_status", "get_market_state":
		return readGrant(ScopeEnvironment), true, true
	case "get_portfolio", "get_positions", "get_recent_orders":
		return readGrant(ScopeAccount), true, true
	case "get_performance", "get_recent_signals":
		return readGrant(ScopeRun), true, true
	case "get_strategy_config":
		return readGrant(ScopeStrategy), true, true
	case "get_risk_status":
		return []operationGrant{{RoleRead, ScopeStrategy}, {RoleRead, ScopeAccount}}, true, true
	case "pause_trading":
		return []operationGrant{{RoleOperate, ScopeEnvironment}}, true, true
	case "prepare_resume", "prepare_config_change", "confirm_action", "cancel_action", "run_backtest", "get_backtest_status", "get_backtest_result":
		return nil, false, true
	default:
		return nil, false, false
	}
}

// needsRun сообщает, требует ли операция заданный run_id.
func needsRun(operation string) bool {
	switch operation {
	case "get_performance", "get_recent_signals", "run_backtest", "get_backtest_status", "get_backtest_result":
		return true
	default:
		return false
	}
}

// needsAccount сообщает, требует ли операция заданный account_ref.
func needsAccount(operation string) bool {
	switch operation {
	case "get_portfolio", "get_positions", "get_recent_orders", "get_risk_status":
		return true
	default:
		return false
	}
}

// readGrant задаёт единственный read grant указанного типа.
func readGrant(kind ScopeType) []operationGrant {
	return []operationGrant{{role: RoleRead, scopeType: kind}}
}
