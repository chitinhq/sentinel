// Package pipeline — end-to-end smoke test.
//
// Issue: chitinhq/sentinel#28
//
// This test proves the sentinel pipeline works from event emission through
// to analyzer findings. It is skipped unless TEST_DATABASE_URL is set,
// so `go test ./...` stays fast for contributors without Docker.
//
// To run locally:
//
//	docker run -d --name postgres-sentinel-test \
//	    -e POSTGRES_PASSWORD=test -e POSTGRES_DB=sentinel_test \
//	    -p 5433:5432 postgres:16-alpine
//	export TEST_DATABASE_URL='postgresql://postgres:test@localhost:5433/sentinel_test?sslmode=disable'
//	go test ./internal/pipeline/... -run TestSmokePipeline -v
package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
	"github.com/chitinhq/sentinel/internal/mcp"
	"github.com/chitinhq/sentinel/internal/pipeline"
)

// governanceEventsSchema is the minimum schema required by internal/mcp
// (IngestFile) and internal/db (QueryEvents and friends). The production
// schema lives in Neon; this test recreates it locally against Docker
// Postgres so we never touch prod data.
//
// Keep this in sync with any columns referenced by queries in
// internal/db/neon.go.
const governanceEventsSchema = `
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
);
CREATE INDEX IF NOT EXISTS idx_gov_events_timestamp ON governance_events (timestamp);
CREATE INDEX IF NOT EXISTS idx_gov_events_action ON governance_events (action);
CREATE INDEX IF NOT EXISTS idx_gov_events_outcome ON governance_events (outcome);
`

const tenantsSchema = `
CREATE TABLE IF NOT EXISTS tenants (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

func TestSmokePipeline(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; see smoke_test.go header for setup")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test db: %v", err)
	}

	// --- Schema setup (idempotent, scoped to this test) -------------------
	for _, stmt := range []string{governanceEventsSchema, tenantsSchema} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}

	// Apply sentinel migrations (execution_events etc.) so Pass 6/7 queries
	// do not fail when Ingestion.Enabled is true.
	migrationsDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	files, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".sql" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(migrationsDir, f.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", f.Name(), err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply migration %s: %v", f.Name(), err)
		}
	}

	// Clean slate for this test — additive only on schema, wipe rows so
	// repeated runs stay deterministic.
	for _, tbl := range []string{"governance_events", "execution_events"} {
		if _, err := pool.Exec(ctx, "TRUNCATE TABLE "+tbl+" RESTART IDENTITY"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	// --- Seed tenant -------------------------------------------------------
	const tenantID = "smoke-test-tenant"
	_, err = pool.Exec(ctx, `
		INSERT INTO tenants (id, name) VALUES ($1, 'Smoke Test Tenant')
		ON CONFLICT (id) DO NOTHING
	`, tenantID)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// --- Write synthetic events.jsonl -------------------------------------
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, "events.jsonl")

	// Timestamp all events "now" so the analyzer's lookback window catches
	// them. The analyzer uses time.Now().Add(-Lookback) as the floor.
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	jsonl := "" +
		`{"ts":"` + ts + `","sid":"smoke-1","agent":"claude-code","tool":"Bash","action":"Bash","command":"ls","outcome":"allow","source":"allowlist","latency_us":120}` + "\n" +
		`{"ts":"` + ts + `","sid":"smoke-1","agent":"claude-code","tool":"Bash","action":"Bash","command":"rm -rf /","outcome":"deny","reason":"dangerous","source":"invariant","latency_us":80}` + "\n" +
		`{"ts":"` + ts + `","sid":"smoke-1","agent":"claude-code","tool":"mcp__octi__sprint_status","action":"mcp__octi__sprint_status","outcome":"deny","reason":"rate_limited","source":"policy","latency_us":55}` + "\n"

	if err := os.WriteFile(eventsPath, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}

	// --- Ingest via internal/mcp.IngestFile -------------------------------
	n, err := mcp.IngestFile(pool, eventsPath, tenantID)
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 events ingested, got %d", n)
	}

	// Sanity check rows landed.
	var rowCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM governance_events").Scan(&rowCount); err != nil {
		t.Fatalf("count governance_events: %v", err)
	}
	if rowCount != 3 {
		t.Fatalf("expected 3 rows in governance_events, got %d", rowCount)
	}

	// --- Run analyzer against the same DB ---------------------------------
	neon, err := db.NewNeonClient(ctx, dbURL)
	if err != nil {
		t.Fatalf("NewNeonClient: %v", err)
	}
	defer neon.Close()

	cfg := smokeConfig()
	store := pipeline.NewStoreAdapter(neon)
	p := pipeline.New(cfg, store, &passthroughInterpreter{}, &mockMemory{}, &mockGitHub{})

	result, err := p.Analyze(ctx)
	if err != nil {
		t.Fatalf("pipeline Analyze: %v", err)
	}
	if result == nil {
		t.Fatal("Analyze returned nil result")
	}

	// --- Assertions --------------------------------------------------------
	if result.TotalFindings == 0 {
		t.Fatal("expected at least one finding from seeded deny events, got zero")
	}

	foundHotspot := false
	for _, f := range result.Interpreted {
		if f.Finding.Pass == "hotspot" {
			foundHotspot = true
			break
		}
	}
	if !foundHotspot {
		t.Errorf("expected at least one hotspot finding; passes seen: %v",
			passesFromResult(result))
	}

	t.Logf("smoke pipeline produced %d findings (high=%d medium=%d low=%d)",
		result.TotalFindings, result.HighConfidence,
		result.MediumConfidence, result.LowConfidence)
}

func passesFromResult(r *pipeline.RunResult) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range r.Interpreted {
		if !seen[f.Finding.Pass] {
			seen[f.Finding.Pass] = true
			out = append(out, f.Finding.Pass)
		}
	}
	return out
}

func smokeConfig() *config.Config {
	return &config.Config{
		Analysis: config.AnalysisConfig{
			Lookback:    24 * time.Hour,
			TrendWindow: 7 * 24 * time.Hour,
		},
		Detection: config.DetectionConfig{
			FalsePositive: config.FalsePositiveConfig{
				MinSampleSize:         5,
				DeviationThreshold:    2.0,
				AbsoluteRateThreshold: 0.5,
			},
			Bypass: config.BypassConfig{
				Window:     5 * time.Minute,
				MinRetries: 3,
			},
			Anomaly: config.AnomalyConfig{
				VolumeSpikeThreshold: 3.0,
			},
		},
		Routing: config.RoutingConfig{
			HighConfidence:   0.75,
			MediumConfidence: 0.40,
			DedupSimilarity:  0.85,
			DedupLookback:    7 * 24 * time.Hour,
		},
		Interpreter: config.InterpreterConfig{
			Model:               "claude-sonnet-4-6",
			MaxFindingsPerBatch: 20,
		},
		GitHub: config.GitHubConfig{
			Repo:   "mock/repo",
			Labels: []string{"sentinel"},
		},
	}
}
