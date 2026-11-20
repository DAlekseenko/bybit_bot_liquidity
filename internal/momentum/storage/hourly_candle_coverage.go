package storage

import (
	"errors"
	"time"
)

// HourlyCandleCoverageStatus описывает покрытие уже материализованного локального окна.
type HourlyCandleCoverageStatus string

const (
	HourlyCandleCovered     HourlyCandleCoverageStatus = "covered"
	HourlyCandleEmpty       HourlyCandleCoverageStatus = "empty"
	HourlyCandleShort       HourlyCandleCoverageStatus = "short_prefix"
	HourlyCandleGap         HourlyCandleCoverageStatus = "gap"
	HourlyCandleUnconfirmed HourlyCandleCoverageStatus = "unconfirmed"
)

// CheckHourlyCandleCoverage проверяет timestamps и Confirmed одной переданной membership.
func CheckHourlyCandleCoverage(key CandleStream, start, end time.Time, rows []StoredCandle) (HourlyCandleCoverageStatus, error) {
	const hourMS int64 = 3600000
	step, err := candleKey(key)
	if err != nil || step != hourMS || !candleTime(start, hourMS) || !candleTime(end, hourMS) || !start.Before(end) {
		return "", errors.Join(ErrCandles, ErrCandleArgument)
	}
	startMS, endMS := start.UnixMilli(), end.UnixMilli()
	expectedCount := (endMS - startMS) / hourMS
	if int64(len(rows)) > expectedCount {
		return "", errors.Join(ErrCandles, ErrCandleArgument)
	}
	if len(rows) == 0 {
		return HourlyCandleEmpty, nil
	}
	gap, unconfirmed := false, false
	for i, row := range rows {
		stamp := row.OpenTime
		if !candleTime(stamp, hourMS) || stamp.Before(start) || !stamp.Before(end) ||
			(i > 0 && !rows[i-1].OpenTime.Before(stamp)) {
			return "", errors.Join(ErrCandles, ErrCandleArgument)
		}
		wanted := endMS - int64(len(rows)-i)*hourMS
		gap = gap || stamp.UnixMilli() != wanted
		unconfirmed = unconfirmed || !row.Confirmed
	}
	switch {
	case unconfirmed:
		return HourlyCandleUnconfirmed, nil
	case gap:
		return HourlyCandleGap, nil
	case int64(len(rows)) < expectedCount:
		return HourlyCandleShort, nil
	default:
		return HourlyCandleCovered, nil
	}
}
