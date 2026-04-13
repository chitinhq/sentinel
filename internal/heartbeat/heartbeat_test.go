package heartbeat

import (
	"context"
	"errors"
	"testing"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name      string
		count     int
		threshold int
		wantPage  bool
	}{
		{"below threshold pages", 2, 10, true},
		{"zero pages", 0, 10, true},
		{"at threshold green", 10, 10, false},
		{"above threshold green", 2800, 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.count, tc.threshold)
			if got.Paging != tc.wantPage {
				t.Fatalf("Paging = %v, want %v (count=%d threshold=%d)",
					got.Paging, tc.wantPage, tc.count, tc.threshold)
			}
			if got.Count != tc.count || got.Threshold != tc.threshold {
				t.Errorf("unexpected decision echo: %+v", got)
			}
		})
	}
}

type fakeCounter struct {
	count int
	err   error
}

func (f fakeCounter) CountLast24h(ctx context.Context) (int, error) {
	return f.count, f.err
}

type fakeNotifier struct {
	calls   int
	title   string
	body    string
	prio    string
	failErr error
}

func (f *fakeNotifier) Notify(ctx context.Context, title, body, priority string) error {
	f.calls++
	f.title = title
	f.body = body
	f.prio = priority
	return f.failErr
}

func TestRun_Paging(t *testing.T) {
	n := &fakeNotifier{}
	dec, err := Run(context.Background(), fakeCounter{count: 3}, n, 10)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !dec.Paging {
		t.Fatalf("expected paging decision, got %+v", dec)
	}
	if n.calls != 1 {
		t.Fatalf("notifier calls = %d, want 1", n.calls)
	}
	if n.prio != "urgent" {
		t.Errorf("priority = %q, want urgent", n.prio)
	}
}

func TestRun_Green(t *testing.T) {
	n := &fakeNotifier{}
	dec, err := Run(context.Background(), fakeCounter{count: 2800}, n, 10)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dec.Paging {
		t.Fatalf("expected green decision, got %+v", dec)
	}
	if n.calls != 1 {
		t.Fatalf("notifier calls = %d, want 1", n.calls)
	}
}

func TestRun_CounterError(t *testing.T) {
	n := &fakeNotifier{}
	_, err := Run(context.Background(), fakeCounter{err: errors.New("db down")}, n, 10)
	if err == nil {
		t.Fatal("expected error on counter failure")
	}
	if n.calls != 0 {
		t.Errorf("notifier should not be called on count error, calls=%d", n.calls)
	}
}

func TestRun_NilNotifier(t *testing.T) {
	// A nil notifier is allowed — useful for dry runs / tests.
	dec, err := Run(context.Background(), fakeCounter{count: 0}, nil, 10)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !dec.Paging {
		t.Fatalf("expected paging, got %+v", dec)
	}
}
