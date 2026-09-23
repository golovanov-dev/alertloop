-- Worker heartbeat (0.7.0).
--
-- One row: when a delivery worker last polled the queue. GET /v1/stats reports
-- it, so a monitor can tell a stopped worker from an idle one. The api and the
-- worker are often separate processes, which is why it lives in the database.
-- Nothing existing changes.
CREATE TABLE IF NOT EXISTS worker_heartbeat (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    last_tick_at TEXT NOT NULL
);
