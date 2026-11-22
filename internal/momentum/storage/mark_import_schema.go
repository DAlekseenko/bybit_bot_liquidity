package storage

// momentumMarkImportProgressSchema хранит курсор импорта mark price отдельно от
// trade-свечей: те же состояния и причины остановки, что у candle_import_progress,
// но mark никогда не подменяется trade close и прогресс рядов не смешивается.
const momentumMarkImportProgressSchema = `CREATE TABLE momentum.mark_import_progress (
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
