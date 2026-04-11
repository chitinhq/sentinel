package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/chitinhq/sentinel/internal/mcp"
)

func main() {
	// Database URL — from env or passed by the MCP client config
	dbURL := os.Getenv("NEON_DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "sentinel-mcp: NEON_DATABASE_URL is required")
		os.Exit(1)
	}

	// Tenant ID — identifies whose events these are
	tenantID := os.Getenv("SENTINEL_TENANT_ID")
	if tenantID == "" {
		tenantID = "00000000-0000-0000-0000-000000000000" // default single-tenant
	}

	// Connect to Neon
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sentinel-mcp: database connection failed: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "sentinel-mcp: database ping failed: %v\n", err)
		os.Exit(1)
	}

	// Connect to Redis (optional — observability tools degrade gracefully)
	redisURL := os.Getenv("SENTINEL_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	var rs *mcp.RedisStore
	if opt, err := redis.ParseURL(redisURL); err == nil {
		redisClient := redis.NewClient(opt)
		if err := redisClient.Ping(context.Background()).Err(); err == nil {
			rs = mcp.NewRedisStore(redisClient)
			defer redisClient.Close()
			fmt.Fprintln(os.Stderr, "sentinel-mcp: redis connected")
		} else {
			fmt.Fprintf(os.Stderr, "sentinel-mcp: redis unavailable (%v) — skip list/budget tools disabled\n", err)
		}
	}

	// Build MCP server
	server := mcp.New()
	mcp.RegisterTools(server, pool, tenantID)

	// Register observability tools (health, query, failures, trends, skip list, budget)
	qs := mcp.NewQueryStore(pool)
	mcp.RegisterObservabilityTools(server, qs, rs)

	fmt.Fprintln(os.Stderr, "sentinel-mcp: ready (stdio)")

	// Run the stdio JSON-RPC loop
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sentinel-mcp: %v\n", err)
		os.Exit(1)
	}
}
