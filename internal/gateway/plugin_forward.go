package gateway

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

const (
	pluginRequestHeader = "X-Magpie-Plugin-Token"
	pluginCallHeader    = "X-Magpie-Plugin-Call"
)

type pluginForwardKey struct{}
type pluginChildKey struct{}

type pluginForward struct {
	mu        sync.Mutex
	providers map[string]bool
}

func (f *pluginForward) mark(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.providers == nil {
		f.providers = map[string]bool{}
	}
	f.providers[id] = true
}

func (f *pluginForward) wraps(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.providers[id]
}

type pluginForwardGrant struct {
	parent                               *pluginForward
	provider                             string
	caller                               caller
	session, subagent, parentAgent, kind string
	claimed                              atomic.Bool
}

type pluginForwardChild struct {
	grant *pluginForwardGrant
	kind  string
}

var pluginForwards sync.Map

// acceptPluginForward consumes metadata only for a live plugin fetch. It
// never changes access.Caller: the inner request still needs its service key.
func acceptPluginForward(r *http.Request, c caller) (*http.Request, caller) {
	token, kind := r.Header.Get(pluginRequestHeader), r.Header.Get(pluginCallHeader)
	r.Header.Del(pluginRequestHeader)
	r.Header.Del(pluginCallHeader)
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || proxied(r) || access.Caller(r.Context()).KeyID == "" {
		return r, c
	}
	v, ok := pluginForwards.Load(token)
	if !ok {
		return r, c
	}
	g := v.(*pluginForwardGrant)
	if kind != "business" && kind != "prewalk_classify" && kind != "prewalk_health" {
		return r, c
	}
	if kind == "business" && !g.claimed.CompareAndSwap(false, true) {
		return r, c
	}
	if kind == "business" {
		c = g.caller
		c.session = g.session
		if c.ua == "" {
			r.Header.Del("User-Agent")
		} else {
			r.Header.Set("User-Agent", c.ua)
		}
		// A plugin may explicitly choose a routing session (including a
		// recursion sentinel). Do not replace that choice with the origin.
		if sessionOf(r.Header) == "" {
			if g.session != "" {
				r.Header.Set(SessionHeader, g.session)
			}
		}
		r.Header.Del("X-Claude-Code-Agent-Id")
		r.Header.Del("X-Claude-Code-Parent-Agent-Id")
		if g.subagent != "" {
			r.Header.Set("X-Claude-Code-Agent-Id", g.subagent)
		}
		if g.parentAgent != "" {
			r.Header.Set("X-Claude-Code-Parent-Agent-Id", g.parentAgent)
		}
	} else {
		c = caller{agent: "prewalk", ua: "prewalk/1"}
		r.Header.Set("User-Agent", c.ua)
		r.Header.Del(SessionHeader)
		for _, h := range sessionHeaders {
			r.Header.Del(h)
		}
	}
	return r.WithContext(context.WithValue(r.Context(), pluginChildKey{}, pluginForwardChild{g, kind})), c
}

// beginPluginForward gives each request its own accounting state. A business
// child marks only the provider fetch it belongs to, never a fallback vendor.
func beginPluginForward(r *http.Request) *http.Request {
	if child, ok := r.Context().Value(pluginChildKey{}).(pluginForwardChild); ok && child.kind == "business" {
		child.grant.parent.mark(child.grant.provider)
	}
	return r.WithContext(context.WithValue(r.Context(), pluginForwardKey{}, &pluginForward{}))
}

func pluginForwardKind(r *http.Request, fallback string) string {
	if child, ok := r.Context().Value(pluginChildKey{}).(pluginForwardChild); ok {
		if child.kind == "business" {
			return child.grant.kind
		}
		return child.kind
	}
	return fallback
}

func pluginForwardSession(r *http.Request, fallback string) string {
	if child, ok := r.Context().Value(pluginChildKey{}).(pluginForwardChild); ok && child.kind == "business" {
		return child.grant.session
	}
	return fallback
}

func pluginWrapped(ctx context.Context, id string) bool {
	f, ok := ctx.Value(pluginForwardKey{}).(*pluginForward)
	return ok && f.wraps(id)
}

// tellPluginForward grants correlation until the plugin returns its response
// headers. Its child requests must begin before then; the token is never saved.
func tellPluginForward(ctx context.Context, p provider.Provider, in http.Header, h http.Header) func() {
	f, ok := ctx.Value(pluginForwardKey{}).(*pluginForward)
	if !ok {
		return func() {}
	}
	c, _ := ctx.Value(callerCtx{}).(caller)
	sub, parent := subagentOf(in)
	g := &pluginForwardGrant{parent: f, provider: p.ID, caller: c, session: sessionOf(in), subagent: sub, parentAgent: parent,
		kind: pluginForwardKind((&http.Request{Header: in}).WithContext(ctx), requestCallKind(in, requestSessionMetadata(in, nil)))}
	token := randomToken()
	pluginForwards.Store(token, g)
	h.Set(pluginRequestHeader, token)
	return func() { pluginForwards.Delete(token) }
}
