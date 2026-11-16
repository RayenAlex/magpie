package budget

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func TestProviderWrapperPreservesCallerTokenBudget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	Forget()
	t.Cleanup(Forget)
	zero := 0.0
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"wrapper/route": {Input: &zero, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
	}}); err != nil {
		t.Fatal(err)
	}
	key := access.Key{ID: "entry", Limit: &access.Limit{Period: "day", Tokens: 100}}
	now := time.Now()
	initial := Of(key, now)
	if initial == nil || initial.Calls != 0 {
		t.Fatalf("initial: %+v", initial)
	}
	Append(usage.Record{Time: now, Operation: usage.OperationProviderWrapper, CallerKeyID: key.ID,
		Provider: "wrapper", Model: "route", Input: 7, Output: 3, Status: 200})
	for _, cold := range []bool{false, true} {
		if cold {
			Forget()
		}
		got := Of(key, now)
		if got == nil || got.Calls != 1 || got.Tokens != 10 || got.Cost != 0 || got.Unpriced != 0 {
			t.Fatalf("cold=%v: caller budget lost: %+v", cold, got)
		}
	}
	_, total, _ := usage.Ledger(usage.All, usage.Filter{})
	if total.Calls != 0 || total.AllTokens() != 0 {
		t.Fatalf("wrapper counted globally: %+v", total)
	}
}
