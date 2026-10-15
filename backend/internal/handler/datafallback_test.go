package handler

import (
	"encoding/json"
	"testing"
)

// The Tencent fqkline response puts forward-adjusted bars under "qfq"+cat
// (qfqday/qfqweek/qfqmonth). An earlier revision probed keys like "dayqfqday"
// that the API never returns, silently breaking the whole fallback path.

func TestTencentKLineNodeKeySelection(t *testing.T) {
	day := json.RawMessage(`[["2024-01-02","10.0","10.5","10.8","9.9","1000","11000"]]`)
	cases := []struct {
		name string
		node map[string]json.RawMessage
		cat  string
		want json.RawMessage
	}{
		{"qfq day key", map[string]json.RawMessage{"qfqday": day}, "day", day},
		{"qfq week key", map[string]json.RawMessage{"qfqweek": day}, "week", day},
		{"qfq month key", map[string]json.RawMessage{"qfqmonth": day}, "month", day},
		{"raw fallback", map[string]json.RawMessage{"day": day}, "day", day},
		{"empty qfq falls back to raw", map[string]json.RawMessage{"qfqday": json.RawMessage(""), "day": day}, "day", day},
		{"missing keys", map[string]json.RawMessage{"other": day}, "day", nil},
		{"empty node", map[string]json.RawMessage{}, "week", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tencentKLineNode(tc.node, tc.cat)
			if tc.want == nil {
				if ok {
					t.Fatalf("expected no array, got %s", got)
				}
				return
			}
			if !ok {
				t.Fatalf("expected array %s, got none", tc.want)
			}
			if string(got) != string(tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBarsFromTencentRows(t *testing.T) {
	rows := [][]string{
		// first bar: no prevClose, change_pct comes from r[8] (9-field row)
		{"2024-01-02", "10.0", "10.5", "10.8", "9.9", "1000", "11000", "", "4.76"},
		// chained: (12.0-10.5)/10.5 = 14.29%
		{"2024-01-03", "10.5", "12.0", "12.2", "10.4", "1200", "13200"},
		// close 0 -> skipped
		{"2024-01-04", "12.0", "0", "12.2", "10.4", "900", "9900"},
		// only 5 fields -> skipped (r[5] must not be read, it would panic)
		{"2024-01-05", "12.0", "12.5"},
	}
	bars := barsFromTencentRows(rows)
	if len(bars) != 2 {
		t.Fatalf("expected 2 bars, got %d", len(bars))
	}
	if bars[0]["date"] != "2024-01-02" || bars[0]["close"] != 10.5 || bars[0]["volume"] != 100000.0 {
		t.Fatalf("unexpected first bar: %v", bars[0])
	}
	if bars[0]["change_pct"] != 4.76 {
		t.Fatalf("first bar change_pct should come from r[8], got %v", bars[0]["change_pct"])
	}
	if bars[1]["close"] != 12.0 || bars[1]["volume"] != 120000.0 || bars[1]["change_pct"] != 14.29 {
		t.Fatalf("unexpected second bar: %v", bars[1])
	}
}

func TestBarsFromTencentRowsEmpty(t *testing.T) {
	if bars := barsFromTencentRows(nil); len(bars) != 0 {
		t.Fatalf("expected no bars, got %d", len(bars))
	}
}

func TestFetchPeriodKLineFallbackMinuteUnsupported(t *testing.T) {
	if _, _, err := fetchPeriodKLineFallback("600519", "5min", 10); err == nil {
		t.Fatal("expected error for minute-level fallback")
	}
}
