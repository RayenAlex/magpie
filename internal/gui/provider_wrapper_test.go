package gui

import (
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

func TestContextCountsProviderForwardingChildOnce(t *testing.T) {
	child := ctxRoute(1, "claude", "s", 12000, 1000, 200000, 10000)
	wrapper := ctxRoute(2, "claude", "s", 12000, 1000, 200000, 10000)
	wrapper.ProviderWrapper = true
	out := contextOf([]gateway.Route{child, wrapper}, 7)
	if len(out.Agents) != 1 || len(out.Sessions) != 1 {
		t.Fatalf("context: %+v", out)
	}
	if out.Agents[0].Calls != 1 || out.Agents[0].Requests != 1 || out.Sessions[0].Requests != 1 {
		t.Fatalf("wrapper counted: agent=%+v session=%+v", out.Agents[0], out.Sessions[0])
	}
}
