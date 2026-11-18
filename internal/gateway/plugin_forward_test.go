package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

func TestPluginForwardScoutReplayReceipt(t *testing.T) {
	for _, mode := range []string{"selected", "unfinished", "foreign", "business-wins", "expired", "error", "missing"} {
		t.Run(mode, func(t *testing.T) {
			parent := &pluginForward{}
			ctx := context.WithValue(t.Context(), pluginForwardKey{}, parent)
			headers := http.Header{}
			done := tellPluginForward(ctx, provider.Provider{ID: "wrapper"}, http.Header{}, headers)
			defer done()
			request := func(kind string) *http.Request {
				r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
				r.RemoteAddr = "127.0.0.1:1122"
				r.Header.Set(pluginRequestHeader, headers.Get(pluginRequestHeader))
				r.Header.Set(pluginCallHeader, kind)
				r = r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: "service"}))
				r, _ = acceptPluginForward(r, caller{})
				return beginPluginForward(r)
			}
			child := request("prewalk_scout")
			reply := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
			tellPluginScoutReceipt(child, reply.Header)
			const receiptHeader = "X-Magpie-Plugin-Replay-Token"
			if reply.Header.Get(receiptHeader) == "" || parent.wraps("wrapper") || pluginForwardKind(child, "") != "prewalk_classify" {
				t.Fatal("scout did not obtain a receipt independently of business forwarding")
			}
			if mode != "unfinished" {
				finishPluginScoutReceipt(child)
			}
			switch mode {
			case "foreign":
				other := http.Header{}
				defer tellPluginForward(ctx, provider.Provider{ID: "wrapper"}, http.Header{}, other)()
				headers.Set(pluginRequestHeader, other.Get(pluginRequestHeader))
				finishPluginScoutReceipt(request("prewalk_scout"))
			case "business-wins":
				request("business")
			case "expired":
				done()
			case "error":
				reply.StatusCode = http.StatusBadGateway
			case "missing":
				reply.Header.Del(receiptHeader)
			}
			acceptPluginScoutReplay(headers.Get(pluginRequestHeader), reply)
			if reply.Header.Get(receiptHeader) != "" {
				t.Fatal("private receipt was not stripped")
			}
			if got, want := parent.wraps("wrapper"), mode == "selected" || mode == "business-wins"; got != want {
				t.Fatalf("deduplicated = %v, want %v", got, want)
			}
			if mode == "selected" && request("business").Context().Value(pluginChildKey{}) != nil {
				t.Fatal("selected scout left the business slot available")
			}
			if mode != "selected" && mode != "business-wins" && mode != "expired" && request("business").Context().Value(pluginChildKey{}) == nil {
				t.Fatal("rejected scout consumed the business slot")
			}
		})
	}
}

func TestPluginForwardGrantBoundaries(t *testing.T) {
	t.Setenv("MAGPIE_TRUST_PROXY", "0")
	parent := &pluginForward{}
	ctx := context.WithValue(t.Context(), pluginForwardKey{}, parent)
	ctx = context.WithValue(ctx, callerCtx{}, caller{agent: "claude", ua: "unidentified-sdk/1"})
	headers := http.Header{}
	done := tellPluginForward(ctx, provider.Provider{ID: "wrapper"}, http.Header{SessionHeader: []string{"original-session"}}, headers)
	t.Cleanup(done)
	token := headers.Get(pluginRequestHeader)
	if token == "" {
		t.Fatal("no grant")
	}
	request := func(remote, kind string, identified, proxy bool) *http.Request {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.RemoteAddr = remote
		r.Header.Set(pluginRequestHeader, token)
		r.Header.Set(pluginCallHeader, kind)
		if proxy {
			r.Header.Set("X-Forwarded-For", "203.0.113.2")
		}
		if identified {
			r = r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: "service"}))
		}
		return r
	}
	for _, tc := range []struct {
		name, remote, kind string
		identified, proxy  bool
	}{
		{"remote", "203.0.113.2:10", "business", true, false},
		{"remote-scout", "203.0.113.2:10", "prewalk_scout", true, false},
		{"unidentified-scout", "127.0.0.1:10", "prewalk_scout", false, false},
		{"proxy-scout", "127.0.0.1:10", "prewalk_scout", true, true},
		{"unidentified", "127.0.0.1:10", "business", false, false},
		{"proxy", "127.0.0.1:10", "business", true, true},
		{"unknown-purpose", "127.0.0.1:10", "hide", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := acceptPluginForward(request(tc.remote, tc.kind, tc.identified, tc.proxy), caller{agent: "other"})
			if c.agent != "other" || r.Context().Value(pluginChildKey{}) != nil {
				t.Fatal("untrusted request consumed grant")
			}
			if r.Header.Get(pluginRequestHeader) != "" {
				t.Fatal("grant leaked after ingress")
			}
		})
	}
	aux, c := acceptPluginForward(request("127.0.0.1:10", "prewalk_health", true, false), caller{})
	aux = beginPluginForward(aux)
	if c.agent != "prewalk" || pluginForwardKind(aux, "") != "prewalk_health" || parent.wraps("wrapper") {
		t.Fatal("auxiliary call consumed business grant or lost purpose")
	}
	child, c := acceptPluginForward(request("127.0.0.1:10", "business", true, false), caller{agent: "Bun"})
	child = beginPluginForward(child)
	if c.agent != "claude" || child.Header.Get(SessionHeader) != "original-session" || !parent.wraps("wrapper") {
		t.Fatal("business source, session or wrapper correlation lost")
	}
	if agentOf(child) != "claude" {
		t.Fatal("routing source differs from restored source")
	}
	child.Header.Set("X-Session-Id", "routing-sentinel")
	child.Header.Del(SessionHeader)
	if pluginForwardSession(child, sessionOf(child.Header)) != "original-session" || sessionOf(child.Header) != "routing-sentinel" {
		t.Fatal("routing and accounting sessions were conflated")
	}
	if access.Caller(child.Context()).KeyID != "service" {
		t.Fatal("source replaced authorization")
	}
	if parent.wraps("fallback") {
		t.Fatal("unrelated fallback became wrapper")
	}
	replay, _ := acceptPluginForward(request("127.0.0.1:10", "business", true, false), caller{})
	if replay.Context().Value(pluginChildKey{}) != nil {
		t.Fatal("business grant replayed")
	}
	done()
	late, _ := acceptPluginForward(request("127.0.0.1:10", "prewalk_health", true, false), caller{})
	if late.Context().Value(pluginChildKey{}) != nil {
		t.Fatal("expired grant accepted")
	}
	if _, ok := pluginForwards.Load(token); ok {
		t.Fatal("grant leaked")
	}
}
