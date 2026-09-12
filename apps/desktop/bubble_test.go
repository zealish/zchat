package main

import (
	"strings"
	"testing"
)

func TestSpacerRun(t *testing.T) {
	// A 20-space sample measuring 80px puts one space at 4px.
	const (
		sample  = 20
		sampled = 80
	)

	for _, tc := range []struct {
		name    string
		needed  int
		sampled int
		sample  int
		spaces  int
	}{
		{"timestamp only", 32, sampled, sample, 11},
		{"timestamp with ticks", 52, sampled, sample, 16},
		{"wide timestamp", 120, sampled, sample, 33},
		{"unmeasured body", 32, 0, sample, 0},
		{"unmeasured timestamp", 0, sampled, sample, 0},
		{"negative sample", 32, sampled, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spacerRun(tc.needed, tc.sampled, tc.sample)
			if tc.spaces == 0 {
				if got != "" {
					t.Fatalf("got %q, want empty", got)
				}
				return
			}
			if !strings.HasPrefix(got, "\u200b") {
				t.Errorf("got %q, want a leading zero-width space", got)
			}
			if n := strings.Count(got, "\u00a0"); n != tc.spaces {
				t.Errorf("got %d spaces, want %d", n, tc.spaces)
			}
		})
	}
}

// The reserved run must actually cover the timestamp, otherwise the overlay
// would sit on top of the body text.
func TestSpacerRunCoversTimestamp(t *testing.T) {
	const (
		sample  = 20
		sampled = 80
		unit    = sampled / sample
	)
	for needed := 1; needed <= 200; needed++ {
		run := spacerRun(needed, sampled, sample)
		if width := strings.Count(run, "\u00a0") * unit; width < needed {
			t.Fatalf("needed %d: reserved %d", needed, width)
		}
	}
}
