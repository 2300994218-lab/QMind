package handler

import (
	"testing"
	"time"
)

func TestNextSessionOpen(t *testing.T) {
	loc := time.Local
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "weekday morning",
			now:  time.Date(2026, 10, 8, 8, 0, 0, 0, loc),
			want: time.Date(2026, 10, 8, 9, 30, 0, 0, loc),
		},
		{
			name: "after close",
			now:  time.Date(2026, 10, 8, 16, 0, 0, 0, loc),
			want: time.Date(2026, 10, 9, 9, 30, 0, 0, loc),
		},
		{
			name: "friday evening skips the weekend",
			now:  time.Date(2026, 10, 9, 20, 0, 0, 0, loc),
			want: time.Date(2026, 10, 12, 9, 30, 0, 0, loc),
		},
		{
			name: "saturday",
			now:  time.Date(2026, 10, 10, 10, 0, 0, 0, loc),
			want: time.Date(2026, 10, 12, 9, 30, 0, 0, loc),
		},
		{
			name: "exactly at open",
			now:  time.Date(2026, 10, 8, 9, 30, 0, 0, loc),
			want: time.Date(2026, 10, 9, 9, 30, 0, 0, loc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextSessionOpen(tc.now); !got.Equal(tc.want) {
				t.Fatalf("nextSessionOpen(%v) = %v, want %v", tc.now.Format(time.RFC3339), got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
