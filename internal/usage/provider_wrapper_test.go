package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestProviderWrapperKeepsAuditButCountsNoConsumption(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	wrapper := Record{Time: now, Operation: OperationProviderWrapper, Agent: "claude", Provider: "wrapper", Model: "route",
		Input: 7, Output: 3, CacheRead: 11, CacheWrite: 13, Status: 200, Session: "s", CallerKeyID: "entry"}
	real := wrapper
	real.Operation, real.Provider, real.CallerKeyID = "", "real", "service"
	aux := Record{Time: now, Agent: "prewalk", Provider: "real", Model: "m", Kind: "prewalk_health", Input: 1, Output: 1, Status: 200}
	for _, r := range []Record{wrapper, real, aux} {
		Append(r)
	}
	if loaded := Load(time.Time{}); len(loaded) != 3 || !loaded[0].IsWrapper() || loaded[0].Input != 7 {
		t.Fatalf("audit rows lost: %+v", loaded)
	}
	price := catalog.Price{Input: 1000, Output: 1000}
	if wrapper.CostAt(price) != 0 {
		t.Fatal("wrapper charged at nonzero price")
	}
	var count Totals
	count.add(wrapper, &price)
	count.addRow(Row{Record: wrapper, Cost: 100, Priced: true})
	if count.Calls != 0 || count.AllTokens() != 0 || count.Cost != 0 {
		t.Fatalf("wrapper counted: %+v", count)
	}
	for _, read := range []func() Totals{
		func() Totals { _, sum, _ := Ledger(All, Filter{}); return sum },
		func() Totals { return Summarize(All).Totals },
		func() Totals { return SummarizeTop(All, 10).Totals },
		func() Totals { return QueryPage(All, Filter{}, 0, 20).Sum },
	} {
		s := read()
		if s.Calls != 2 || s.Input != 8 || s.Output != 4 || s.CacheRead != 11 || s.CacheWrite != 13 {
			t.Errorf("actual plus auxiliary usage: %+v", s)
		}
	}
	heat := HeatmapOf(Filter{})
	if len(heat.Days) != 1 || heat.Days[0].Calls != 2 || heat.Days[0].Tokens != 36 {
		t.Errorf("heatmap: %+v", heat)
	}
	ss := GatewaySessions(time.Time{}, nil)
	if len(ss) != 1 || len(ss[0].Models) != 1 || ss[0].Models[0].Model != "route" || ss[0].Input != 7 || ss[0].Output != 3 {
		t.Errorf("gateway session: %+v", ss)
	}
	// The indexed page still exposes the wrapper's original usage and mark.
	page := QueryPage(All, Filter{Provider: "wrapper"}, 0, 20)
	if len(page.Rows) != 1 || !page.Rows[0].IsWrapper() || page.Rows[0].Input != 7 || page.Rows[0].Cost != 0 {
		t.Errorf("indexed audit: %+v", page.Rows)
	}
}
