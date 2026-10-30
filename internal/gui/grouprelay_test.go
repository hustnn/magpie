package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Routing view's editor doesn't edit relays yet: a save that doesn't
// name them keeps the group's, less those naming a model it took out, and
// one that names them sets them.
func TestGroupSaveKeepsRelays(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"big", "small", "det"}}); err != nil {
		t.Fatal(err)
	}
	relays := []provider.Relay{{From: "a/big", To: "a/small"}}
	if err := provider.SaveGroup(provider.Group{ID: "coder", Name: "Coder", Members: []string{"a/big", "a/small"}, Relays: relays, Detector: "a/det", RelayFrom: 4}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	save := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	save(`{"id":"coder","name":"Coder renamed","members":["a/big","a/small","a/det"]}`)
	if g, _, _ := provider.FindGroup("group/coder"); g.Name != "Coder renamed" || !slices.Equal(g.Relays, relays) || g.Detector != "a/det" || g.RelayFrom != 4 {
		t.Fatalf("not kept: %+v", g)
	}
	// a model taken out takes its relay with it
	save(`{"id":"coder","name":"Coder","members":["a/big","a/det"]}`)
	if g, _, _ := provider.FindGroup("group/coder"); g.Relayed() || g.Detector != "" {
		t.Fatalf("kept with its model gone: %+v", g)
	}
	// named, they are what is saved
	save(`{"id":"coder","name":"Coder","members":["a/big","a/small"],"relays":[{"from":"a/big","to":"a/small"}],"detector":"a/det"}`)
	save(`{"id":"coder","name":"Coder","members":["a/big","a/small"],"relays":[]}`)
	if g, _, _ := provider.FindGroup("group/coder"); g.Relayed() {
		t.Fatalf("relays named empty kept: %+v", g.Relays)
	}
}
