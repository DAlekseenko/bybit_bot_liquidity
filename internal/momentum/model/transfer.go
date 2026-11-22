package model

import (
	"errors"
	"strings"
	"time"
)

var (
	// ErrInvalidTransfer означает некорректный внешний перевод.
	ErrInvalidTransfer = errors.New("invalid external transfer")
	// ErrInvalidMarginMovement означает некорректное перемещение маржи внутри счёта.
	ErrInvalidMarginMovement = errors.New("invalid margin movement")
)

// TransferDirection задаёт направление внешнего перевода относительно счёта.
type TransferDirection string

const (
	// TransferIn — поступление USDT на счёт извне.
	TransferIn TransferDirection = "TRANSFER_IN"
	// TransferOut — вывод USDT со счёта во внешний мир.
	TransferOut TransferDirection = "TRANSFER_OUT"
)

// ExternalTransfer описывает подтверждённый перевод USDT между счётом и внешним миром.
// AmountUSDT всегда положителен, знак задаёт Direction.
type ExternalTransfer struct {
	TransferID    string
	Direction     TransferDirection
	TransferredAt time.Time
	AmountUSDT    Decimal
}

// TransferPosting содержит изменение wallet от внешнего перевода; позиции не меняются.
type TransferPosting struct {
	Transfer      ExternalTransfer
	CashDeltaUSDT Decimal
}

// MarginMovement описывает перенос средств между wallet и маржой позиции внутри счёта.
// Положительная AmountUSDT — из wallet в маржу позиции, отрицательная — обратно.
// Такое движение не меняет ни equity, ни net transfers и не является ExternalTransfer.
type MarginMovement struct {
	MovementID string
	Symbol     string
	MovedAt    time.Time
	AmountUSDT Decimal
}

// ProjectExternalTransfer вычисляет изменение wallet от проверенного внешнего перевода.
func ProjectExternalTransfer(transfer ExternalTransfer) (TransferPosting, error) {
	if !validExternalTransfer(transfer) {
		return TransferPosting{}, ErrInvalidTransfer
	}
	transfer.TransferredAt = transfer.TransferredAt.Round(0).UTC()
	cash := transfer.AmountUSDT
	if transfer.Direction == TransferOut {
		var err error
		if cash, err = Zero().Sub(transfer.AmountUSDT); err != nil {
			return TransferPosting{}, ErrLedgerArithmetic
		}
	}
	return TransferPosting{Transfer: transfer, CashDeltaUSDT: cash}, nil
}

// validExternalTransfer проверяет ID, направление, время и положительную точную сумму.
func validExternalTransfer(t ExternalTransfer) bool {
	return t.TransferID != "" && strings.TrimSpace(t.TransferID) == t.TransferID &&
		(t.Direction == TransferIn || t.Direction == TransferOut) && !t.TransferredAt.IsZero() &&
		t.AmountUSDT.valid() && t.AmountUSDT.value.Sign() > 0
}

// validMarginMovement проверяет ID, символ, время и ненулевую точную сумму.
func validMarginMovement(m MarginMovement) bool {
	return m.MovementID != "" && strings.TrimSpace(m.MovementID) == m.MovementID &&
		m.Symbol != "" && strings.TrimSpace(m.Symbol) == m.Symbol && !m.MovedAt.IsZero() &&
		m.AmountUSDT.valid() && m.AmountUSDT.value.Sign() != 0
}
