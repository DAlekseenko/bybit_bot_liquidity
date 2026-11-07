package storage

const momentumMarketSchema = `CREATE TABLE momentum.mark_prices (
 environment TEXT NOT NULL,
 dataset_id TEXT NOT NULL,
 symbol TEXT NOT NULL CHECK (symbol ~ '^[A-Z0-9]+USDT$'),
 interval_code TEXT NOT NULL CHECK (interval_code IN ('D', '60')),
 open_time TIMESTAMPTZ NOT NULL CHECK (isfinite(open_time)
   AND open_time >= TIMESTAMPTZ '1970-01-01 00:00:00+00'
   AND date_trunc(CASE interval_code WHEN 'D' THEN 'day' ELSE 'hour' END, open_time, 'UTC') = open_time),
 open NUMERIC NOT NULL CHECK (open > 0 AND open < 'Infinity'::NUMERIC),
 high NUMERIC NOT NULL CHECK (high > 0 AND high < 'Infinity'::NUMERIC),
 low NUMERIC NOT NULL CHECK (low > 0 AND low < 'Infinity'::NUMERIC),
 close NUMERIC NOT NULL CHECK (close > 0 AND close < 'Infinity'::NUMERIC),
 confirmed BOOLEAN NOT NULL,
 CHECK (high >= greatest(open, close) AND low <= least(open, close) AND high >= low),
 PRIMARY KEY (environment, dataset_id, symbol, interval_code, open_time),
 FOREIGN KEY (environment, dataset_id) REFERENCES momentum.datasets (environment, dataset_id)
);
CREATE TABLE momentum.funding (
 environment TEXT NOT NULL,
 dataset_id TEXT NOT NULL,
 symbol TEXT NOT NULL CHECK (symbol ~ '^[A-Z0-9]+USDT$'),
 funding_time TIMESTAMPTZ NOT NULL CHECK (isfinite(funding_time)
   AND funding_time >= TIMESTAMPTZ '1970-01-01 00:00:00+00'
   AND date_trunc('milliseconds', funding_time, 'UTC') = funding_time),
 rate NUMERIC NOT NULL CHECK (rate > '-Infinity'::NUMERIC AND rate < 'Infinity'::NUMERIC),
 PRIMARY KEY (environment, dataset_id, symbol, funding_time),
 FOREIGN KEY (environment, dataset_id) REFERENCES momentum.datasets (environment, dataset_id)
);`
