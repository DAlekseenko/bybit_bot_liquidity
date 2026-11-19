package storage

const momentumFundingImportProgressSchema = `CREATE TABLE momentum.funding_import_progress (
 environment TEXT NOT NULL,
 dataset_id TEXT NOT NULL,
 symbol TEXT NOT NULL CHECK (symbol ~ '^[A-Z0-9]+USDT$'),
 lower_time TIMESTAMPTZ NOT NULL CHECK (isfinite(lower_time)
   AND lower_time >= TIMESTAMPTZ '1970-01-01 00:00+00'
   AND date_trunc('milliseconds',lower_time,'UTC') = lower_time),
 upper_time TIMESTAMPTZ NOT NULL CHECK (isfinite(upper_time)
   AND upper_time >= lower_time AND upper_time > TIMESTAMPTZ '1970-01-01 00:00+00'
   AND upper_time <= TIMESTAMPTZ '294276-12-31 23:59:59.999+00'
   AND date_trunc('milliseconds',upper_time,'UTC') = upper_time),
 next_end TIMESTAMPTZ CHECK (isfinite(next_end)
   AND next_end >= lower_time AND next_end <= upper_time
   AND next_end > TIMESTAMPTZ '1970-01-01 00:00+00'
   AND date_trunc('milliseconds',next_end,'UTC') = next_end),
 revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
 action TEXT NOT NULL DEFAULT 'pending' CHECK (action IN ('pending','continue','lower_reached','empty','source_boundary')),
 last_request_end TIMESTAMPTZ CHECK (isfinite(last_request_end)
   AND last_request_end >= lower_time AND last_request_end <= upper_time
   AND last_request_end > TIMESTAMPTZ '1970-01-01 00:00+00'
   AND date_trunc('milliseconds',last_request_end,'UTC') = last_request_end),
 last_server_time TIMESTAMPTZ CHECK (isfinite(last_server_time)
   AND last_server_time > TIMESTAMPTZ '1970-01-01 00:00+00'
   AND last_server_time <= TIMESTAMPTZ '294276-12-31 23:59:59.999+00'
   AND date_trunc('milliseconds',last_server_time,'UTC') = last_server_time),
 last_page_count INTEGER CHECK (last_page_count BETWEEN 0 AND 200),
 last_page_oldest TIMESTAMPTZ CHECK (isfinite(last_page_oldest)
   AND last_page_oldest >= lower_time AND last_page_oldest <= last_request_end
   AND last_page_oldest <= last_server_time
   AND date_trunc('milliseconds',last_page_oldest,'UTC') = last_page_oldest),
 CHECK ((action = 'pending' AND revision = 0 AND next_end IS NOT NULL AND next_end = upper_time
     AND last_request_end IS NULL AND last_server_time IS NULL
     AND last_page_count IS NULL AND last_page_oldest IS NULL)
   OR (action <> 'pending' AND revision > 0 AND last_request_end IS NOT NULL
     AND last_server_time IS NOT NULL AND last_page_count IS NOT NULL)),
 CHECK (action = 'pending'
   OR (action = 'continue' AND last_page_count > 0 AND last_page_oldest IS NOT NULL
     AND next_end IS NOT NULL AND next_end = last_page_oldest - INTERVAL '1 millisecond'
     AND next_end < last_request_end)
   OR (action = 'lower_reached' AND last_page_count > 0 AND last_page_oldest IS NOT NULL
     AND last_page_oldest = lower_time AND next_end IS NULL)
   OR (action = 'empty' AND last_page_count = 0 AND last_page_oldest IS NULL AND next_end IS NULL)
   OR (action = 'source_boundary' AND last_page_count > 0 AND last_page_oldest IS NOT NULL
     AND lower_time = TIMESTAMPTZ '1970-01-01 00:00+00'
     AND last_page_oldest = TIMESTAMPTZ '1970-01-01 00:00:00.001+00' AND next_end IS NULL)),
 PRIMARY KEY (environment,dataset_id,symbol,lower_time,upper_time),
 FOREIGN KEY (environment,dataset_id) REFERENCES momentum.datasets(environment,dataset_id)
);`
