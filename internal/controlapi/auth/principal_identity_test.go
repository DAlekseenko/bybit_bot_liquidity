package auth

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestServicePrincipal проверяет ошибки, enum, копирование и нулевое значение.
func TestServicePrincipal(t *testing.T) {
	good := Grant{Role: RoleRead, ScopeType: ScopeStrategy, Scope: Scope{StrategyID: "策略", Environment: EnvironmentTestnet}}
	for _, tc := range []struct {
		name        string
		id, request string
		grants      []Grant
		want        error
	}{
		{"principal precedence", "", "", []Grant{{}}, ErrInvalidArgument},
		{"request", "svc", "bad*id", nil, ErrInvalidArgument},
		{"identifier before enum", "svc", "req", []Grant{{Role: "bad", ScopeType: "bad", Scope: Scope{StrategyID: "*"}}}, ErrInvalidArgument},
		{"environment before role", "svc", "req", []Grant{{Role: "bad", ScopeType: "bad", Scope: Scope{StrategyID: "s", Environment: "bad"}}}, ErrUnsupported},
		{"role before scope type", "svc", "req", []Grant{{Role: "bad", ScopeType: "bad", Scope: Scope{StrategyID: "s", Environment: EnvironmentPaper}}}, ErrUnsupported},
		{"unknown scope type", "svc", "req", []Grant{{Role: RoleRead, ScopeType: "unknown", Scope: Scope{StrategyID: "s", Environment: EnvironmentPaper}}}, ErrUnsupported},
		{"empty scope type", "svc", "req", []Grant{{Role: RoleRead, ScopeType: "", Scope: Scope{StrategyID: "s", Environment: EnvironmentPaper}}}, ErrUnsupported},
		{"valid after invalid", "svc", "req", []Grant{good, {Role: "bad", ScopeType: ScopeRun, Scope: good.Scope}}, ErrUnsupported},
	} {
		p, err := NewServicePrincipal(tc.id, tc.request, tc.grants)
		if !errors.Is(err, tc.want) || p.Kind() != "" || p.grants != nil || p.principalID != "" || p.requestID != "" || p.actor != (Actor{}) {
			t.Fatalf("case=%s principal=%v err=%v", tc.name, p, err)
		}
	}
	for _, role := range []Role{RoleRead, RoleOperate, RoleAdmin} {
		if _, err := NewServicePrincipal("svc", "req", []Grant{{Role: role, ScopeType: ScopeStrategy, Scope: good.Scope}}); err != nil {
			t.Fatalf("role %q: %v", role, err)
		}
	}
	for _, scopeType := range []ScopeType{ScopeStrategy, ScopeEnvironment, ScopeRun, ScopeAccount} {
		if _, err := NewServicePrincipal("svc", "req", []Grant{{Role: RoleRead, ScopeType: scopeType, Scope: good.Scope}}); err != nil {
			t.Fatalf("scope type %q: %v", scopeType, err)
		}
	}
	for _, environment := range []Environment{EnvironmentPaper, EnvironmentTestnet, EnvironmentBacktest} {
		grant := good
		grant.Scope.Environment = environment
		if _, err := NewServicePrincipal("svc", "req", []Grant{grant}); err != nil {
			t.Fatalf("environment %q: %v", environment, err)
		}
	}
	if _, err := NewServicePrincipal("svc", "req", make([]Grant, 101)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
	hundred := make([]Grant, 100)
	for i := range hundred {
		hundred[i] = good
	}
	if _, err := NewServicePrincipal("svc", "req", hundred); err != nil {
		t.Fatalf("100 grants rejected: %v", err)
	}
	if _, err := NewServicePrincipal("", "", []Grant{good}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
	grants := []Grant{good}
	p, err := NewServicePrincipal("svc", "req", grants)
	if err != nil || p.Kind() != PrincipalService {
		t.Fatalf("principal=%v err=%v", p, err)
	}
	if actor, ok := p.Actor(); ok || actor != (Actor{}) {
		t.Fatalf("actor=%v ok=%v", actor, ok)
	}
	run, account := "run", "acct"
	grants = []Grant{{Role: RoleRead, ScopeType: ScopeStrategy, Scope: Scope{StrategyID: "s", Environment: EnvironmentPaper, RunID: &run, AccountRef: &account}}}
	p, err = NewServicePrincipal("svc", "req", grants)
	if err != nil {
		t.Fatal(err)
	}
	run = "mutated"
	account = "mutated"
	if *p.grants[0].Scope.RunID != "run" || *p.grants[0].Scope.AccountRef != "acct" {
		t.Fatal("service grant was not deeply copied")
	}
}

// TestIdentityValues проверяет human снимок, валидаторы и JSON fail-closed.
func TestIdentityValues(t *testing.T) {
	run := "run"
	input := []Grant{{Role: RoleAdmin, ScopeType: ScopeRun, Scope: Scope{StrategyID: "strat", Environment: EnvironmentPaper, RunID: &run}}}
	// Тестовый объект имитирует закрытую проверку и сам proof не проверяет.
	identity := HumanIdentity{actor: Actor{Platform: "chat", UserID: "user", ChatID: "room"}, verified: true}
	p, err := NewHumanPrincipal(identity, "req", input)
	if err != nil {
		t.Fatal(err)
	}
	*input[0].Scope.RunID = "changed"
	if p.Kind() != PrincipalHuman || p.grants[0].Scope.RunID == nil || *p.grants[0].Scope.RunID != "run" {
		t.Fatalf("principal snapshot lost: %+v", p)
	}
	if actor, ok := p.Actor(); !ok || actor != identity.actor {
		t.Fatalf("actor=%v ok=%v", actor, ok)
	}
	if _, err := NewHumanPrincipal(HumanIdentity{}, "", nil); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err := NewHumanPrincipal(HumanIdentity{actor: Actor{Platform: "*", UserID: "u", ChatID: "c"}, verified: true}, "", nil); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("invalid actor precedence lost")
	}
	if _, err := NewHumanPrincipal(identity, "", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
	before := p
	if err := json.Unmarshal([]byte(`{"kind":"service"}`), &p); !errors.Is(err, ErrUnauthenticated) || p.Kind() != before.Kind() {
		t.Fatalf("principal=%+v err=%v", p, err)
	}
	if !validID(strings.Repeat("界", 128)) || validID(strings.Repeat("a", 129)) || validID("a\xff") || validID("*") {
		t.Fatal("identifier validation mismatch")
	}
	for _, scope := range []Scope{{StrategyID: "", Environment: EnvironmentPaper}, {StrategyID: "s", RunID: stringPtr("*")}, {StrategyID: "s", AccountRef: stringPtr("")}} {
		if !errors.Is(validateScope(scope), ErrInvalidArgument) {
			t.Fatalf("scope accepted: %+v", scope)
		}
	}
	for _, environment := range []Environment{"", "LIVE", "paper"} {
		if !errors.Is(validateScope(Scope{StrategyID: "s", Environment: environment}), ErrUnsupported) {
			t.Fatalf("environment accepted: %q", environment)
		}
	}
	base := Scope{StrategyID: "s", Environment: EnvironmentPaper, RunID: stringPtr("run"), AccountRef: stringPtr("acct")}
	variants := []Scope{
		{StrategyID: "other", Environment: base.Environment, RunID: base.RunID, AccountRef: base.AccountRef},
		{StrategyID: base.StrategyID, Environment: EnvironmentBacktest, RunID: base.RunID, AccountRef: base.AccountRef},
		{StrategyID: base.StrategyID, Environment: base.Environment, RunID: stringPtr("other"), AccountRef: base.AccountRef},
		{StrategyID: base.StrategyID, Environment: base.Environment, RunID: base.RunID, AccountRef: stringPtr("other")},
		{StrategyID: base.StrategyID, Environment: base.Environment, AccountRef: base.AccountRef},
		{StrategyID: base.StrategyID, Environment: base.Environment, RunID: base.RunID},
	}
	for _, variant := range variants {
		if equalScope(base, variant) {
			t.Fatalf("different scopes compare equal: %+v", variant)
		}
	}
	if !equalScope(base, cloneScope(base)) {
		t.Fatal("cloned scope differs")
	}
	if cloneString(nil) != nil || !validNullable(nil) {
		t.Fatal("nil handling mismatch")
	}
}

// stringPtr создаёт указатель на строку для табличных значений теста.
func stringPtr(value string) *string { return &value }
