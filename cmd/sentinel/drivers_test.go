package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFmtUptime(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0s"},
		{-5, "0s"},
		{12, "12s"},
		{59, "59s"},
		{60, "1m"},
		{2700, "45m"},
		{3600, "1h"},
		{3720, "1h2m"},
		{7320, "2h2m"},
		{22320, "6h12m"}, // matches the spec sample output
	}
	for _, c := range cases {
		if got := fmtUptime(c.in); got != c.want {
			t.Errorf("fmtUptime(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDriverStatus(t *testing.T) {
	now := time.Date(2026, 4, 13, 2, 0, 0, 0, time.UTC)
	threshold := 120 * time.Second

	if s := driverStatus(nil, now, threshold); s != "NEVER" {
		t.Errorf("nil heartbeat: got %q, want NEVER", s)
	}

	fresh := now.Add(-30 * time.Second)
	if s := driverStatus(&fresh, now, threshold); s != "OK" {
		t.Errorf("fresh heartbeat: got %q, want OK", s)
	}

	stale := now.Add(-15 * time.Minute)
	got := driverStatus(&stale, now, threshold)
	if got != "STALE (15m)" {
		t.Errorf("stale heartbeat: got %q, want STALE (15m)", got)
	}

	// Just over the boundary still counts as stale.
	edge := now.Add(-121 * time.Second)
	if s := driverStatus(&edge, now, threshold); s == "OK" {
		t.Errorf("edge case (121s > threshold): got OK, want STALE")
	}
}

// TestDriversRollup is the integration test: seed a handful of heartbeat
// events into a test DB and verify queryDrivers collapses them correctly.
// Skipped without TEST_DATABASE_URL so contributors without Docker don't
// see a red test.
func TestDriversRollup(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; see internal/pipeline/smoke_test.go for setup")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Minimal schema — matches smoke_test.go but kept local so this test
	// is self-contained.
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS governance_events (
		    id              BIGSERIAL PRIMARY KEY,
		    tenant_id       TEXT NOT NULL,
		    session_id      TEXT NOT NULL,
		    agent_id        TEXT NOT NULL,
		    event_type      TEXT NOT NULL,
		    action          TEXT NOT NULL,
		    resource        TEXT,
		    outcome         TEXT,
		    risk_level      TEXT DEFAULT 'low',
		    event_source    TEXT,
		    driver_type     TEXT,
		    policy_version  TEXT,
		    metadata        JSONB,
		    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE governance_events RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	now := time.Now().UTC()
	seed := []struct {
		action string
		host   string
		uptime int64
		model  string
		ts     time.Time
	}{
		{"flow.driver.octi.heartbeat", "ubuntu-32gb-hil-1", 3600, "", now.Add(-10 * time.Second)},
		{"flow.driver.octi.heartbeat", "ubuntu-32gb-hil-1", 3660, "", now.Add(-5 * time.Second)},
		{"flow.driver.openclaw.heartbeat", "ubuntu-32gb-hil-1", 120, "qwen-2.5-coder-7b", now.Add(-8 * time.Second)},
		{"tool.Bash", "", 0, "", now}, // unrelated event — must NOT appear in rollup
	}
	for _, s := range seed {
		md := map[string]any{}
		if s.host != "" {
			md["host"] = s.host
		}
		if s.uptime > 0 {
			md["uptime_seconds"] = s.uptime
		}
		if s.model != "" {
			md["model"] = s.model
		}
		mdJSON, _ := json.Marshal(md)
		_, err := pool.Exec(ctx, `
			INSERT INTO governance_events
				(tenant_id, session_id, agent_id, event_type, action, outcome, event_source, driver_type, metadata, timestamp)
			VALUES ('t', 's', 'a', 'tool_call', $1, 'allow', 'heartbeat', 'a', $2::jsonb, $3)
		`, s.action, string(mdJSON), s.ts)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	rows, err := queryDrivers(ctx, pool)
	if err != nil {
		t.Fatalf("queryDrivers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 driver rows, got %d: %+v", len(rows), rows)
	}

	byDriver := map[string]driverRow{}
	for _, r := range rows {
		byDriver[r.Driver] = r
	}

	octi, ok := byDriver["octi"]
	if !ok {
		t.Fatalf("missing octi row")
	}
	if octi.Host != "ubuntu-32gb-hil-1" {
		t.Errorf("octi host = %q", octi.Host)
	}
	if octi.UptimeSeconds == nil || *octi.UptimeSeconds != 3660 {
		t.Errorf("octi uptime = %v, want 3660", octi.UptimeSeconds)
	}

	oc, ok := byDriver["openclaw"]
	if !ok {
		t.Fatalf("missing openclaw row")
	}
	if oc.Model != "qwen-2.5-coder-7b" {
		t.Errorf("openclaw model = %q", oc.Model)
	}

	if _, ok := byDriver["tool"]; ok {
		t.Errorf("non-heartbeat event leaked into rollup")
	}
}
