package storage

const momentumMarketCandlesSchema = `CREATE TABLE momentum.datasets (
 environment TEXT NOT NULL CHECK (environment IN ('mainnet', 'testnet')),
 dataset_id TEXT NOT NULL CHECK (length(dataset_id) BETWEEN 1 AND 128 AND btrim(dataset_id) = dataset_id),
 source TEXT NOT NULL CHECK (source = 'bybit_v5'),
 category TEXT NOT NULL CHECK (category = 'linear'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
   CHECK (isfinite(created_at) AND created_at >= TIMESTAMPTZ '1970-01-01 00:00:00+00'),
 PRIMARY KEY (environment, dataset_id)
);
CREATE TABLE momentum.candles (
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
 volume NUMERIC NOT NULL CHECK (volume >= 0 AND volume < 'Infinity'::NUMERIC),
 confirmed BOOLEAN NOT NULL,
 CHECK (high >= greatest(open, close) AND low <= least(open, close) AND high >= low),
 PRIMARY KEY (environment, dataset_id, symbol, interval_code, open_time),
 FOREIGN KEY (environment, dataset_id) REFERENCES momentum.datasets (environment, dataset_id)
);`
