package main

import "testing"

func TestFmtSafety(t *testing.T) {
	cases := []struct {
		allow, total int
		want         string
	}{
		{0, 0, "—"},        // divide-by-zero guard
		{0, 10, "0.00"},    // all denied
		{10, 10, "1.00"},   // all allowed
		{7, 10, "0.70"},
		{1, 3, "0.33"},     // truncation/rounding
		{5, -1, "—"},       // negative denom guard
	}
	for _, c := range cases {
		if got := fmtSafety(c.allow, c.total); got != c.want {
			t.Errorf("fmtSafety(%d,%d) = %q, want %q", c.allow, c.total, got, c.want)
		}
	}
}

func TestFmtRating(t *testing.T) {
	cases := []struct {
		sum     float64
		samples int
		want    string
	}{
		{0, 0, "—"},
		{4.5, 1, "4.5"},
		{9.2, 2, "4.6"},
		{13.8, 3, "4.6"},
		{10, -2, "—"},
	}
	for _, c := range cases {
		if got := fmtRating(c.sum, c.samples); got != c.want {
			t.Errorf("fmtRating(%v,%d) = %q, want %q", c.sum, c.samples, got, c.want)
		}
	}
}

func TestFmtFindingsPerSession(t *testing.T) {
	cases := []struct {
		findings, sessions int
		want               string
	}{
		{0, 0, "—"},
		{0, 5, "0.0"},
		{10, 5, "2.0"},
		{7, 3, "2.3"},
	}
	for _, c := range cases {
		if got := fmtFindingsPerSession(c.findings, c.sessions); got != c.want {
			t.Errorf("fmtFindingsPerSession(%d,%d) = %q, want %q", c.findings, c.sessions, got, c.want)
		}
	}
}
