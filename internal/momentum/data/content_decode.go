package data

import (
	"bytes"
	"encoding/json"
	"time"

	"bybit_bot_liquidity/internal/momentum/model"
)

// DecodeFundingContent восстанавливает только точный канонический funding v1 payload.
func DecodeFundingContent(raw []byte) (FundingContentInput, error) {
	var wire fundingContentWire
	if json.Unmarshal(raw, &wire) != nil {
		return FundingContentInput{}, ErrFundingContent
	}
	input := FundingContentInput{
		Environment: wire.Environment, DatasetID: wire.DatasetID, Source: wire.Source, Category: wire.Category,
		Symbol: wire.Symbol, Endpoint: wire.Endpoint, Start: time.UnixMilli(wire.StartMs).UTC(), End: time.UnixMilli(wire.EndMs).UTC(),
		Events: make([]model.FundingEvent, len(wire.Events)),
	}
	for i, event := range wire.Events {
		rate, err := model.ParseRate(event.Rate)
		if err != nil {
			return FundingContentInput{}, ErrFundingContent
		}
		input.Events[i] = model.FundingEvent{FundingTime: time.UnixMilli(event.FundingTimeMs).UTC(), Rate: rate}
	}
	encoded, _, err := EncodeFundingContent(input)
	if err != nil || !bytes.Equal(raw, encoded) {
		return FundingContentInput{}, ErrFundingContent
	}
	return input, nil
}

// DecodeTradeCandleContent восстанавливает канонический trade v1 без изменения row policy.
func DecodeTradeCandleContent(raw []byte) (TradeCandleContentInput, error) {
	var wire tradeContentWire
	if json.Unmarshal(raw, &wire) != nil {
		return TradeCandleContentInput{}, ErrTradeCandleContent
	}
	input := TradeCandleContentInput{
		Environment: wire.Environment, DatasetID: wire.DatasetID, Source: wire.Source, Category: wire.Category,
		Symbol: wire.Symbol, IntervalCode: wire.Interval, Endpoint: wire.Endpoint,
		Start: time.UnixMilli(wire.StartMs).UTC(), End: time.UnixMilli(wire.EndMs).UTC(),
		Rows: make([]TradeCandleContentRow, len(wire.Candles)),
	}
	for i, candle := range wire.Candles {
		row := TradeCandleContentRow{OpenTime: time.UnixMilli(candle.OpenTimeMs).UTC(), Confirmed: candle.Confirmed}
		fields := []*model.Decimal{&row.Open, &row.High, &row.Low, &row.Close, &row.Volume}
		for j, value := range []string{candle.Open, candle.High, candle.Low, candle.Close, candle.Volume} {
			parse := model.ParsePrice
			if j == 4 {
				parse = model.ParseQty
			}
			parsed, err := parse(value)
			if err != nil {
				return TradeCandleContentInput{}, ErrTradeCandleContent
			}
			*fields[j] = parsed
		}
		input.Rows[i] = row
	}
	encoded, _, err := EncodeTradeCandleContent(input)
	if err != nil || !bytes.Equal(raw, encoded) {
		return TradeCandleContentInput{}, ErrTradeCandleContent
	}
	return input, nil
}

// DecodeMarkPriceContent восстанавливает канонический mark v1 без trade fallback.
func DecodeMarkPriceContent(raw []byte) (MarkPriceContentInput, error) {
	var wire markContentWire
	if json.Unmarshal(raw, &wire) != nil {
		return MarkPriceContentInput{}, ErrMarkPriceContent
	}
	input := MarkPriceContentInput{
		Environment: wire.Environment, DatasetID: wire.DatasetID, Source: wire.Source, Category: wire.Category,
		Symbol: wire.Symbol, IntervalCode: wire.Interval, Endpoint: wire.Endpoint,
		Start: time.UnixMilli(wire.StartMs).UTC(), End: time.UnixMilli(wire.EndMs).UTC(),
		Rows: make([]model.MarkPriceCandle, len(wire.Candles)),
	}
	for i, candle := range wire.Candles {
		row := model.MarkPriceCandle{OpenTime: time.UnixMilli(candle.OpenTimeMs).UTC(), Confirmed: candle.Confirmed}
		fields := []*model.Decimal{&row.Open, &row.High, &row.Low, &row.Close}
		for j, value := range []string{candle.Open, candle.High, candle.Low, candle.Close} {
			parsed, err := model.ParsePrice(value)
			if err != nil {
				return MarkPriceContentInput{}, ErrMarkPriceContent
			}
			*fields[j] = parsed
		}
		input.Rows[i] = row
	}
	encoded, _, err := EncodeMarkPriceContent(input)
	if err != nil || !bytes.Equal(raw, encoded) {
		return MarkPriceContentInput{}, ErrMarkPriceContent
	}
	return input, nil
}
