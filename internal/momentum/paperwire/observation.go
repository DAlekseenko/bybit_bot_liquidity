package paperwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// EncodeObservation сохраняет momentum/paper-observation/v1 как bare canonical object.
func EncodeObservation(o Observation) ([]byte, error) {
	if _, err := MeasureObservation(o); err != nil {
		return nil, err
	}
	w := observationOut(o)
	if err := orderObservation(&w); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return nil, invalid("observation encode")
	}
	return raw, nil
}

// DecodeObservation проверяет ресурсы до typed arrays и требует точные canonical bytes.
func DecodeObservation(raw []byte) (Observation, error) {
	if err := preflight(raw); err != nil {
		return Observation{}, err
	}
	if err := observationStreamCounts(raw); err != nil {
		return Observation{}, err
	}
	var w observationWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Observation{}, errors.Join(ErrPaperWire, err)
	}
	o := observationIn(w)
	canonical, err := EncodeObservation(o)
	if err != nil {
		return Observation{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return Observation{}, invalid("observation canonical")
	}
	return o, nil
}

// orderObservation сортирует только копии requests; source rows остаются неизменными.
func orderObservation(o *observationWire) error {
	sort.Slice(o.TradeWindows, func(i, j int) bool {
		a, b := o.TradeWindows[i], o.TradeWindows[j]
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.Interval != b.Interval {
			return a.Interval < b.Interval
		}
		if a.StartMS != b.StartMS {
			return a.StartMS < b.StartMS
		}
		return a.EndMS < b.EndMS
	})
	for i, w := range o.TradeWindows {
		if w.StartMS < 0 || w.EndMS < w.StartMS {
			return invalid("trade request bounds")
		}
		if i > 0 {
			p := o.TradeWindows[i-1]
			if p.Symbol == w.Symbol && p.Interval == w.Interval && p.StartMS == w.StartMS && p.EndMS == w.EndMS {
				return invalid("duplicate trade request")
			}
		}
	}
	sort.Slice(o.MarkPages, func(i, j int) bool {
		a, b := o.MarkPages[i], o.MarkPages[j]
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.IntervalCode != b.IntervalCode {
			return a.IntervalCode < b.IntervalCode
		}
		if !a.StartTime.Equal(b.StartTime.Time) {
			return a.StartTime.Before(b.StartTime.Time)
		}
		return a.EndTime.Before(b.EndTime.Time)
	})
	sort.Slice(o.FundingPages, func(i, j int) bool {
		a, b := o.FundingPages[i], o.FundingPages[j]
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if !a.StartTime.Equal(b.StartTime.Time) {
			return a.StartTime.Before(b.StartTime.Time)
		}
		return a.EndTime.Before(b.EndTime.Time)
	})
	for i, p := range o.MarkPages {
		if !requestTimes(p.StartTime.Time, p.EndTime.Time) {
			return invalid("mark request bounds")
		}
		if i > 0 {
			q := o.MarkPages[i-1]
			if q.Symbol == p.Symbol && q.IntervalCode == p.IntervalCode && q.StartTime.Equal(p.StartTime.Time) && q.EndTime.Equal(p.EndTime.Time) {
				return invalid("duplicate mark request")
			}
		}
	}
	for i, p := range o.FundingPages {
		if !requestTimes(p.StartTime.Time, p.EndTime.Time) {
			return invalid("funding request bounds")
		}
		if i > 0 {
			q := o.FundingPages[i-1]
			if q.Symbol == p.Symbol && q.StartTime.Equal(p.StartTime.Time) && q.EndTime.Equal(p.EndTime.Time) {
				return invalid("duplicate funding request")
			}
		}
	}
	return nil
}

// requestTimes проверяет целые миллисекунды bounds без рыночного validator.
func requestTimes(start, end time.Time) bool {
	return start.UTC().Year() >= 1970 && end.UTC().Year() <= 9999 && start.Nanosecond()%int(time.Millisecond) == 0 && end.Nanosecond()%int(time.Millisecond) == 0 && !end.Before(start)
}
