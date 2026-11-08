package store

import (
	"context"
	"testing"
	"time"
)

// TestSnapLogSums: the panel's week of decisions, failures apart.
func TestSnapLogSums(t *testing.T) {
	s := openTest(t, Options{})
	ctx := context.Background()
	for _, l := range []SnapLog{
		{Tier: "free", Kind: "chat", KindConf: 0.9, Applied: []string{"route_light", "chat", "effort_low"}, LatencyMS: 300, SnapTokens: 800, SavedTokens: 9000},
		{Tier: "vip", Kind: "code", KindConf: 0.8, Applied: []string{"route_strong", "focus", "trim"}, LatencyMS: 501, SnapTokens: 900, SavedTokens: 4000},
		{Tier: "free", Error: "timeout", LatencyMS: 0},
	} {
		if err := s.AddSnapLog(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.SnapSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st.Calls != 3 || st.Failures != 1 || st.AvgLatencyMS != 401 || st.MaxLatency != 501 || st.SnapTokens != 1700 || st.SavedTokens != 13000 {
		t.Fatalf("stats: %+v", st)
	}
	if st.Applied["chat"] != 1 || st.Applied["route_strong"] != 1 || st.Applied["trim"] != 1 || st.Kinds["chat"] != 1 || st.Kinds["code"] != 1 || len(st.Kinds) != 2 {
		t.Fatalf("applied %v kinds %v", st.Applied, st.Kinds)
	}
}
