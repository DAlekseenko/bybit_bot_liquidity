// Package auth содержит неизменяемые значения идентичности и полномочий.
package auth

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// PrincipalKind обозначает тип principal.
type PrincipalKind string

// Role задаёт роль в проверенной выдаче.
type Role string

// ScopeType задаёт тип области действия выдачи.
type ScopeType string

// Environment задаёт среду исполнения.
type Environment string

const (
	// PrincipalService обозначает сервисного principal.
	PrincipalService PrincipalKind = "service"
	// PrincipalHuman обозначает proof-backed human principal.
	PrincipalHuman PrincipalKind = "human"
	// RoleRead разрешает чтение в рамках выдачи.
	RoleRead Role = "read"
	// RoleOperate обозначает роль управления.
	RoleOperate Role = "operate"
	// RoleAdmin обозначает административную роль.
	RoleAdmin Role = "admin"
	// ScopeStrategy ограничивает выдачу стратегией.
	ScopeStrategy ScopeType = "strategy"
	// ScopeEnvironment ограничивает выдачу средой.
	ScopeEnvironment ScopeType = "environment"
	// ScopeRun ограничивает выдачу запуском.
	ScopeRun ScopeType = "run"
	// ScopeAccount ограничивает выдачу аккаунтом.
	ScopeAccount ScopeType = "account"
	// EnvironmentPaper задаёт paper-среду.
	EnvironmentPaper Environment = "PAPER"
	// EnvironmentTestnet задаёт среду Bybit Testnet.
	EnvironmentTestnet Environment = "TESTNET"
	// EnvironmentBacktest задаёт среду backtest.
	EnvironmentBacktest Environment = "BACKTEST"
)

var (
	// ErrInvalidArgument сообщает о неверном аргументе конструктора.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrUnauthenticated сообщает об отсутствующей проверенной идентичности.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrUnsupported сообщает о значении enum вне закрытого набора.
	ErrUnsupported = errors.New("unsupported value")
)

// Scope содержит идентификаторы области и nullable селекторы.
type Scope struct {
	StrategyID  string
	Environment Environment
	RunID       *string
	AccountRef  *string
}

// Grant связывает роль с областью действия.
type Grant struct {
	Role      Role
	ScopeType ScopeType
	Scope     Scope
}

// Actor содержит проверенные поля участника.
type Actor struct {
	Platform string
	UserID   string
	ChatID   string
}

// HumanIdentity хранит закрытое доказательство и снимок участника.
type HumanIdentity struct {
	actor    Actor
	verified bool
}

// Principal содержит закрытый неизменяемый снимок полномочий.
type Principal struct {
	kind        PrincipalKind
	principalID string
	requestID   string
	actor       Actor
	grants      []Grant
	valid       bool
}

// NewServicePrincipal проверяет сервисные идентификаторы и копирует выдачи.
func NewServicePrincipal(principalID, requestID string, grants []Grant) (Principal, error) {
	if !validID(principalID) || !validID(requestID) {
		return Principal{}, ErrInvalidArgument
	}
	if err := validateGrants(grants); err != nil {
		return Principal{}, err
	}
	return Principal{kind: PrincipalService, principalID: principalID, requestID: requestID, grants: cloneGrants(grants), valid: true}, nil
}

// NewHumanPrincipal требует приватное проверенное доказательство и копирует выдачи.
func NewHumanPrincipal(identity HumanIdentity, requestID string, grants []Grant) (Principal, error) {
	if !identity.verified || !validActor(identity.actor) {
		return Principal{}, ErrUnauthenticated
	}
	if !validID(requestID) {
		return Principal{}, ErrInvalidArgument
	}
	if err := validateGrants(grants); err != nil {
		return Principal{}, err
	}
	return Principal{kind: PrincipalHuman, requestID: requestID, actor: identity.actor, grants: cloneGrants(grants), valid: true}, nil
}

// Kind возвращает вид principal либо пустое значение для нулевого значения.
func (p Principal) Kind() PrincipalKind {
	if !p.valid {
		return ""
	}
	return p.kind
}

// Actor возвращает копию участника только для валидного human principal.
func (p Principal) Actor() (Actor, bool) {
	if !p.valid || p.kind != PrincipalHuman {
		return Actor{}, false
	}
	return p.actor, true
}

// UnmarshalJSON запрещает создавать полномочия из JSON.
func (p *Principal) UnmarshalJSON([]byte) error {
	return ErrUnauthenticated
}

// validID проверяет UTF-8 идентификатор длиной от 1 до 128 символов без wildcard.
func validID(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 1 && utf8.RuneCountInString(value) <= 128 && !strings.Contains(value, "*")
}

// validNullable проверяет nullable идентификатор, сохраняя различие nil и пустого значения.
func validNullable(value *string) bool {
	return value == nil || validID(*value)
}

// validateScope проверяет идентификаторы области и закрытый набор сред.
func validateScope(scope Scope) error {
	if !validID(scope.StrategyID) || !validNullable(scope.RunID) || !validNullable(scope.AccountRef) {
		return ErrInvalidArgument
	}
	if !validEnvironment(scope.Environment) {
		return ErrUnsupported
	}
	return nil
}

// equalScope сравнивает значения полей с сохранением различия nullable nil.
func equalScope(a, b Scope) bool {
	return a.StrategyID == b.StrategyID && a.Environment == b.Environment && equalNullable(a.RunID, b.RunID) && equalNullable(a.AccountRef, b.AccountRef)
}

// cloneScope глубоко копирует nullable селекторы области.
func cloneScope(scope Scope) Scope {
	scope.RunID = cloneString(scope.RunID)
	scope.AccountRef = cloneString(scope.AccountRef)
	return scope
}

// equalNullable сравнивает nullable строки без сведения nil к пустой строке.
func equalNullable(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// cloneString создаёт независимую копию nullable строки.
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// validActor проверяет все поля участника как отдельные идентификаторы.
func validActor(actor Actor) bool {
	return validID(actor.Platform) && validID(actor.UserID) && validID(actor.ChatID)
}

// validateGrants проверяет весь список в контрактном порядке до его сохранения.
func validateGrants(grants []Grant) error {
	if len(grants) > 100 {
		return ErrInvalidArgument
	}
	for _, grant := range grants {
		if err := validateScope(grant.Scope); err != nil {
			return err
		}
		if !validRole(grant.Role) || !validScopeType(grant.ScopeType) {
			return ErrUnsupported
		}
	}
	return nil
}

// validEnvironment проверяет закрытый набор сред.
func validEnvironment(value Environment) bool {
	return value == EnvironmentPaper || value == EnvironmentTestnet || value == EnvironmentBacktest
}

// validRole проверяет закрытый набор ролей.
func validRole(value Role) bool {
	return value == RoleRead || value == RoleOperate || value == RoleAdmin
}

// validScopeType проверяет закрытый набор типов области.
func validScopeType(value ScopeType) bool {
	return value == ScopeStrategy || value == ScopeEnvironment || value == ScopeRun || value == ScopeAccount
}

// cloneGrants глубоко копирует выдачи и их nullable строки.
func cloneGrants(grants []Grant) []Grant {
	copy := make([]Grant, len(grants))
	for i, grant := range grants {
		grant.Scope = cloneScope(grant.Scope)
		copy[i] = grant
	}
	return copy
}
