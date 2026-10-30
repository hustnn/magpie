package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestGroupRelayCmd(t *testing.T) {
	groupsHome(t)
	if _, err := addGroup("Coder", []string{"models=a/claude-opus-5-5,b/gpt-5.5,a/only-a"}); err != nil {
		t.Fatal(err)
	}
	group := func() provider.Group {
		t.Helper()
		g, err := findGroup("coder")
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	if err := relayCmd([]string{"coder"}); err != nil { // none yet: said, not an error
		t.Fatal(err)
	}
	// a relay needs a detector
	if err := relayCmd([]string{"add", "coder", "claude-opus-5-5", "gpt-5.5"}); err == nil || !strings.Contains(err.Error(), "detector") {
		t.Fatalf("%v", err)
	}
	if err := relayCmd([]string{"add", "coder", "claude-opus-5-5", "gpt-5.5", "detector=a/only-a", "rounds=3"}); err != nil {
		t.Fatal(err)
	}
	g := group()
	if !slices.Equal(g.Relays, []provider.Relay{{From: "a/claude-opus-5-5", To: "b/gpt-5.5"}}) || g.Detector != "a/only-a" || g.RelayFrom != 3 {
		t.Fatalf("%+v", g)
	}
	// a turn handed on to gpt-5.5 isn't handed on from it again
	if err := relayCmd([]string{"add", "coder", "gpt-5.5", "only-a"}); err == nil || !strings.Contains(err.Error(), "moves once") {
		t.Fatalf("chain: %v", err)
	}
	if err := relayCmd([]string{"add", "coder", "claude-opus-5-5", "gpt-5.5", "after=x"}); err == nil {
		t.Fatal("rounds x read")
	}
	if err := relayCmd([]string{"detector", "coder", "b/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if err := relayCmd([]string{"rounds", "coder", "8"}); err != nil {
		t.Fatal(err)
	}
	if g := group(); g.Detector != "b/gpt-5.5" || g.RelayFrom != 8 {
		t.Fatalf("%+v", g)
	}
	// a model taken out of the group takes its relay with it
	if _, err := setGroup("coder", []string{"models-=gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if g := group(); g.Relayed() || g.Detector != "" {
		t.Fatalf("relay kept: %+v", g)
	}
	if err := relayCmd([]string{"add", "coder", "claude-opus-5-5", "only-a", "detector=a/only-a"}); err != nil {
		t.Fatal(err)
	}
	if err := relayCmd([]string{"rm", "coder", "2"}); err == nil {
		t.Fatal("relay 2 removed")
	}
	if err := relayCmd([]string{"rm", "coder", "1"}); err != nil {
		t.Fatal(err)
	}
	if g := group(); g.Relayed() {
		t.Fatalf("%+v", g.Relays)
	}
}
