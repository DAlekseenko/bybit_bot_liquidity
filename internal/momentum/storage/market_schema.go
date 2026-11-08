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

const momentumImportProgressSchema = `CREATE TABLE momentum.candle_import_progress (
 environment TEXT NOT NULL,
 dataset_id TEXT NOT NULL,
 symbol TEXT NOT NULL CHECK (symbol ~ '^[A-Z0-9]+USDT$'),
 interval_code TEXT NOT NULL CHECK (interval_code IN ('D','60')),
 lower_open TIMESTAMPTZ NOT NULL CHECK (isfinite(lower_open)
   AND lower_open >= TIMESTAMPTZ '1970-01-01 00:00+00'),
 upper_open TIMESTAMPTZ NOT NULL CHECK (isfinite(upper_open) AND upper_open > lower_open),
 next_end TIMESTAMPTZ NOT NULL CHECK (isfinite(next_end)),
 revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
 state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','progress','complete','blocked')),
 reason TEXT NOT NULL DEFAULT '' CHECK (reason IN ('','empty','gap','unconfirmed','source_boundary')),
 last_server_time TIMESTAMPTZ CHECK (isfinite(last_server_time)
   AND last_server_time >= TIMESTAMPTZ '1970-01-01 00:00+00'
   AND date_trunc('milliseconds',last_server_time,'UTC') = last_server_time),
 CHECK (date_trunc(CASE interval_code WHEN 'D' THEN 'day' ELSE 'hour' END,lower_open,'UTC') = lower_open
   AND date_trunc(CASE interval_code WHEN 'D' THEN 'day' ELSE 'hour' END,upper_open,'UTC') = upper_open),
 CHECK (next_end >= lower_open - INTERVAL '1 millisecond' AND next_end < upper_open
   AND date_trunc(CASE interval_code WHEN 'D' THEN 'day' ELSE 'hour' END,
     next_end + INTERVAL '1 millisecond','UTC') = next_end + INTERVAL '1 millisecond'),
 CHECK ((state = 'pending' AND revision = 0 AND reason = '' AND last_server_time IS NULL
     AND next_end = upper_open - INTERVAL '1 millisecond')
   OR (state = 'progress' AND revision > 0 AND reason = '' AND last_server_time IS NOT NULL
     AND last_server_time >= upper_open
     AND next_end >= lower_open AND next_end < upper_open - INTERVAL '1 millisecond')
   OR (state = 'complete' AND revision > 0 AND reason = '' AND last_server_time IS NOT NULL
     AND last_server_time >= upper_open
     AND next_end = lower_open - INTERVAL '1 millisecond')
   OR (state = 'blocked' AND revision > 0 AND reason IN ('empty','gap','unconfirmed')
     AND last_server_time IS NOT NULL
     AND next_end >= lower_open AND (last_server_time >= upper_open OR reason = 'unconfirmed'))
   OR (state = 'blocked' AND revision > 0 AND reason = 'source_boundary'
     AND lower_open = TIMESTAMPTZ '1970-01-01 00:00+00' AND last_server_time IS NULL
     AND next_end = upper_open - INTERVAL '1 millisecond')),
 PRIMARY KEY (environment,dataset_id,symbol,interval_code,lower_open,upper_open),
 FOREIGN KEY (environment,dataset_id) REFERENCES momentum.datasets(environment,dataset_id)
);`
