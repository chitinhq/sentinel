// Package heartbeat implements a daily liveness alert for sentinel.
//
// Motivation: on 2026-04-07 our governance_events volume collapsed from
// ~2800/day to a handful. Nobody noticed for a week because there was no
// floor alarm. Heartbeat fixes that — it counts events in the last 24h and
// pages via ntfy when the count drops below a configured threshold.
package heartbeat

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Decision captures the outcome of a heartbeat check.
type Decision struct {
	Count     int
	Threshold int
	Paging    bool // true when count < threshold
}

// Decide is the pure decision function. count below threshold → page.
func Decide(count, threshold int) Decision {
	return Decision{
		Count:     count,
		Threshold: threshold,
		Paging:    count < threshold,
	}
}

// EventCounter abstracts the 24h count query so tests can inject fakes.
type EventCounter interface {
	CountLast24h(ctx context.Context) (int, error)
}

// Notifier delivers the heartbeat message somewhere (ntfy in prod).
type Notifier interface {
	Notify(ctx context.Context, title, body, priority string) error
}

// PoolCounter queries governance_events via a pgx pool.
type PoolCounter struct {
	Pool *pgxpool.Pool
}

func (p *PoolCounter) CountLast24h(ctx context.Context) (int, error) {
	var count int
	err := p.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM governance_events WHERE timestamp >= NOW() - INTERVAL '24 hours'`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count governance_events: %w", err)
	}
	return count, nil
}

// NtfyNotifier posts to https://ntfy.sh/<topic>.
type NtfyNotifier struct {
	Topic  string
	Client *http.Client
}

func NewNtfyNotifier(topic string) *NtfyNotifier {
	return &NtfyNotifier{Topic: topic, Client: &http.Client{Timeout: 5 * time.Second}}
}

func (n *NtfyNotifier) Notify(ctx context.Context, title, body, priority string) error {
	if n.Topic == "" {
		return fmt.Errorf("ntfy topic empty")
	}
	endpoint := fmt.Sprintf("https://ntfy.sh/%s", url.PathEscape(n.Topic))
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	if priority != "" {
		req.Header.Set("Priority", priority)
	}
	client := n.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy status %d", resp.StatusCode)
	}
	return nil
}

// Run performs the heartbeat check: count → decide → notify.
// Returns the decision so callers can set exit status.
func Run(ctx context.Context, counter EventCounter, notifier Notifier, threshold int) (Decision, error) {
	count, err := counter.CountLast24h(ctx)
	if err != nil {
		return Decision{}, err
	}
	dec := Decide(count, threshold)
	var title, body, prio string
	if dec.Paging {
		title = "[sentinel] governance_events volume LOW"
		body = fmt.Sprintf("Only %d events in last 24h (threshold %d). Pipeline likely broken.", dec.Count, dec.Threshold)
		prio = "urgent"
	} else {
		title = "[sentinel] heartbeat OK"
		body = fmt.Sprintf("governance_events last 24h: %d (>= %d).", dec.Count, dec.Threshold)
		prio = "min"
	}
	if notifier != nil {
		if nerr := notifier.Notify(ctx, title, body, prio); nerr != nil {
			// Notification failure is surfaced but does not mask the decision.
			return dec, fmt.Errorf("notify: %w", nerr)
		}
	}
	return dec, nil
}
