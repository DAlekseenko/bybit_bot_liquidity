package storage

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"bybit_bot_liquidity/internal/momentum/model"
)

var (
	// ErrFunding обозначает отказ хранилища funding.
	ErrFunding = errors.New("momentum funding storage failed")
	// ErrFundingArgument обозначает недопустимые аргументы или страницу.
	ErrFundingArgument = errors.New("invalid momentum funding argument")
	// ErrFundingDataset отличает отсутствующий либо неподходящий namespace.
	ErrFundingDataset = errors.New("momentum funding dataset is unavailable")
	// ErrFundingTx обозначает неподходящую транзакцию.
	ErrFundingTx = errors.New("momentum funding write requires a read-write READ COMMITTED transaction")
	// ErrFundingConflict обозначает различающиеся ставки по одному ключу.
	ErrFundingConflict = errors.New("momentum funding conflicts with an immutable row")
)

// FundingStream задаёт namespace одного потока funding без candle interval.
type FundingStream struct {
	Environment string
	DatasetID   string
	Symbol      string
}

var fundingStreamSymbol = regexp.MustCompile(`^[A-Z0-9]+USDT$`)

// validFundingStream проверяет существующие правила ключа без изменения opaque ID.
func validFundingStream(key FundingStream) bool {
	runes := utf8.RuneCountInString(key.DatasetID)
	return (key.Environment == "testnet" || key.Environment == "mainnet") &&
		utf8.ValidString(key.DatasetID) && runes >= 1 && runes <= 128 && strings.Trim(key.DatasetID, " ") == key.DatasetID &&
		!strings.ContainsRune(key.DatasetID, 0) && fundingStreamSymbol.MatchString(key.Symbol)
}

// fundingFailure сохраняет безопасный тип и context без SQL-диагностики.
func fundingFailure(ctx context.Context, kind error) error {
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(ErrFunding, ctx.Err())
	}
	return errors.Join(ErrFunding, kind)
}

// AppendFundingPage добавляет события и оставляет Commit вызывающему коду.
func AppendFundingPage(ctx context.Context, tx *sql.Tx, key FundingStream, page model.FundingPage) (err error) {
	if tx == nil {
		return fundingFailure(ctx, ErrFundingTx)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if ctx == nil || !validFundingStream(key) || !validFundingPage(key, page) {
		return fundingFailure(ctx, ErrFundingArgument)
	}
	if ctx.Err() != nil {
		return fundingFailure(ctx, nil)
	}
	var writable bool
	err = tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('transaction_isolation') = 'read committed'
	 AND pg_catalog.current_setting('transaction_read_only') = 'off'`).Scan(&writable)
	if err != nil {
		return fundingFailure(ctx, nil)
	}
	if !writable {
		return fundingFailure(ctx, ErrFundingTx)
	}
	var source, category string
	err = tx.QueryRowContext(ctx, `SELECT source,category FROM momentum.datasets
	 WHERE environment=$1 AND dataset_id=$2 FOR SHARE`, key.Environment, key.DatasetID).Scan(&source, &category)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (source != page.Source || category != page.Category)) {
		return fundingFailure(ctx, ErrFundingDataset)
	}
	if err != nil {
		return fundingFailure(ctx, nil)
	}
	for i, row := range page.Events {
		stamp := row.FundingTime
		if !candleTime(stamp, 1) || stamp.Before(page.StartTime) || stamp.After(page.EndTime) ||
			stamp.After(page.ServerTime) || (i > 0 && !stamp.Before(page.Events[i-1].FundingTime)) {
			return fundingFailure(ctx, ErrFundingArgument)
		}
		value, err := row.Rate.WireDecimal()
		if err != nil {
			return fundingFailure(ctx, ErrFundingArgument)
		}
		if _, err := model.ParseRate(value); err != nil {
			return fundingFailure(ctx, ErrFundingArgument)
		}
		if err := appendFundingEvent(ctx, tx, key, row, value); err != nil {
			return fundingFailure(ctx, err)
		}
	}
	if ctx.Err() != nil {
		return fundingFailure(ctx, nil)
	}
	return nil
}

// validFundingPage проверяет согласованность сохранённого происхождения и границ.
func validFundingPage(key FundingStream, page model.FundingPage) bool {
	host := "api.bybit.com"
	if key.Environment == "testnet" {
		host = "api-testnet.bybit.com"
	}
	return len(page.Events) >= 1 && len(page.Events) <= 200 && page.Environment == key.Environment &&
		page.Symbol == key.Symbol && page.Source == "bybit_v5" && page.Category == "linear" &&
		page.Endpoint == "https://"+host+"/v5/market/funding/history" &&
		candleTime(page.StartTime, 1) && candleTime(page.EndTime, 1) && candleTime(page.ServerTime, 1) &&
		page.EndTime.UnixMilli() > 0 && page.ServerTime.UnixMilli() > 0 && !page.EndTime.Before(page.StartTime)
}

// appendFundingEvent сохраняет точную ставку либо проверяет неизменённую конфликтную строку.
func appendFundingEvent(ctx context.Context, tx *sql.Tx, key FundingStream, row model.FundingEvent, value string) error {
	args := []any{key.Environment, key.DatasetID, key.Symbol, row.FundingTime.UTC(), value}
	result, err := tx.ExecContext(ctx, `INSERT INTO momentum.funding(environment,dataset_id,symbol,funding_time,rate)
	 VALUES($1,$2,$3,$4,$5) ON CONFLICT(environment,dataset_id,symbol,funding_time) DO NOTHING`, args...)
	if err != nil {
		return ErrFunding
	}
	count, err := result.RowsAffected()
	if err != nil || (count != 0 && count != 1) {
		return ErrFunding
	}
	if count == 1 {
		return nil
	}
	// Новый statement видит строку конкурента, если INSERT ожидал его Commit.
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT rate=$5::numeric FROM momentum.funding
	 WHERE environment=$1 AND dataset_id=$2 AND symbol=$3 AND funding_time=$4 FOR SHARE`, args...).Scan(&same)
	if err != nil {
		return ErrFunding
	}
	if !same {
		return ErrFundingConflict
	}
	return nil
}
