package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/usage"
)

// The fork's scheduled gate exercises the real host and LAN guard without
// another checkout, a real account or a vendor request.
func TestPluginForwardUsageThroughHost(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Cleanup(plugin.Settle)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer vendor-only" {
			t.Error("vendor authorization changed")
		}
		for _, header := range []string{pluginRequestHeader, pluginCallHeader} {
			if r.Header.Get(header) != "" {
				t.Errorf("private correlation header reached vendor: %s", header)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"id":"known-usage","model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"id":"known-usage","model":"model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "real", Name: "Local vendor", Key: "vendor-only", Models: []string{"model"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Forward service", "Entry caller")
	pid := provider.PluginID("fakeco")
	for i, model := range []string{"real/model", pid + "/fake-1"} {
		if _, err := access.Update("models-key", access.Change{Key: keys[i].ID, Models: []string{model}}); err != nil {
			t.Fatal(err)
		}
	}
	zero, one, two := 0.0, 1.0, 2.0
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{
		"real/model":    {Input: &one, Output: &two, CacheRead: &zero, CacheWrite: &zero},
		pid + "/fake-1": {Input: &zero, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	t.Cleanup(gw.Close)
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))
	fake, err := filepath.Abs("../plugin/testdata/fake/index.js")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(filepath.ToSlash(fake))
	entry := filepath.Join(t.TempDir(), "forward.mjs")
	code := `import { FakePlugin } from ` + string(quoted) + `;
export const ForwardPlugin = async (context) => {
  const fake = await FakePlugin(context);
  return { ...fake, auth: { ...fake.auth, loader: async (getAuth) => ({
    apiKey: "unused",
    async fetch(url, init) {
      const auth = await getAuth();
      const headers = new Headers({ "content-type": "application/json", authorization: "Bearer " + auth.key });
      headers.set("x-magpie-plugin-token", new Headers(init.headers).get("x-magpie-plugin-token") || "");
      headers.set("x-magpie-plugin-call", "business");
      const body = { ...JSON.parse(init.body), model: "real/model" };
      return fetch("http://" + process.env.MAGPIE_ADDR + "/v1/chat/completions", { ...init, headers, body: JSON.stringify(body) });
    }
  }) } };
};`
	if err := os.WriteFile(entry, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(t.Context(), "fakeco", 0, nil, secrets[0], ""); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), "POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+pid+`/fake-1","stream":false,"messages":[{"role":"user","content":"synthetic"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secrets[1])
	req.Header.Set("User-Agent", "opencode/1.0")
	req.Header.Set(SessionHeader, "origin-session")
	res, err := gw.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("reply: %d %s %v", res.StatusCode, body, err)
	}
	var reply struct {
		Usage struct {
			Input  int `json:"prompt_tokens"`
			Output int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || reply.Usage.Input != 7 || reply.Usage.Output != 3 {
		t.Fatalf("client usage: %s (%v)", body, err)
	}
	rows, total, _ := usage.Ledger(usage.All, usage.Filter{})
	if calls.Load() != 1 || len(rows) != 2 || total.Calls != 1 || total.Input != 7 || total.Output != 3 || total.Cost < 0.00001299 || total.Cost > 0.00001301 {
		t.Fatalf("forwarding counted incorrectly: vendor=%d rows=%d totals=%+v", calls.Load(), len(rows), total)
	}
	var real, wrapper int
	for _, row := range rows {
		if row.Session != "origin-session" {
			t.Errorf("accounting session: %q", row.Session)
		}
		if row.Provider == "real" && row.Agent == "opencode" && row.CallerKeyID == keys[0].ID && !row.IsWrapper() {
			real++
		}
		if row.Provider == pid && row.IsWrapper() && row.CallerKeyID == keys[1].ID && row.Input == 7 && row.Output == 3 {
			wrapper++
		}
	}
	if real != 1 || wrapper != 1 {
		t.Fatalf("source, identity or audit lost: real=%d wrapper=%d rows=%+v", real, wrapper, rows)
	}
}
