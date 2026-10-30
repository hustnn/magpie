package provider

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanRelays(t *testing.T) {
	members := []string{"a/m", "b/big", "c/x"}
	for _, tc := range []struct {
		relays []Relay
		err    string
	}{
		{[]Relay{{From: "b/big"}}, "relay 1: it needs"},
		{[]Relay{{From: "z/no", To: "a/m"}}, "relay 1: z/no is not in the group"},
		{[]Relay{{From: "b/big", To: "z/no"}}, "relay 1: z/no is not in the group"},
		{[]Relay{{From: "a/m", To: "a/m"}}, "to itself"},
		{[]Relay{{From: "b/big", To: "a/m"}, {From: "b/big", To: "c/x"}}, "relay 2: b/big hands its turns on already"},
		// a chain would move a turn twice
		{[]Relay{{From: "b/big", To: "a/m"}, {From: "a/m", To: "c/x"}}, "a/m is handed turns on to, so it can't hand them on itself"},
	} {
		if _, err := cleanRelays(tc.relays, members); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%+v: %v, want …%s", tc.relays, err, tc.err)
		}
	}
	got, err := cleanRelays([]Relay{{From: " b/big ", To: "a/m "}, {From: "c/x", To: "a/m"}}, members)
	if err != nil || !slices.Equal(got, []Relay{{From: "b/big", To: "a/m"}, {From: "c/x", To: "a/m"}}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSaveGroupRelays(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "big"}}); err != nil {
			t.Fatal(err)
		}
	}
	relays := []Relay{{From: "b/big", To: "a/m"}}
	for _, tc := range []struct {
		detector, err string
	}{
		{"", "needs the group's detector"},
		{"group/other", "no group"},
		{"group/r", "its own detector"},
		{"z/nothing", "knows no model"},
	} {
		err := SaveGroup(Group{Name: "R", Members: []string{"b/big", "a/m"}, Relays: relays, Detector: tc.detector})
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: %v, want …%s", tc.detector, err, tc.err)
		}
	}
	if err := SaveGroup(Group{Name: "R", Members: []string{"b/big", "a/m"}, Relays: relays, Detector: " magpie/a/m ", RelayFrom: -3}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup(GroupPrefix + "r")
	if g.Detector != "a/m" || !slices.Equal(g.Relays, relays) || g.RelayFrom != 0 || g.RelayRounds() != RelayFromDefault {
		t.Fatalf("%+v", g)
	}
	if r, ok := g.RelayOf("b/big"); !ok || r.To != "a/m" {
		t.Fatalf("relay of b/big: %+v %v", r, ok)
	}
	if _, ok := g.RelayOf("a/m"); ok {
		t.Fatal("a/m hands on")
	}
	// a manual group's relays wait, as its rules do
	g.Routing = Manual
	if g.Live().Relayed() {
		t.Fatal("a manual group hands turns on")
	}
	// a member renamed (its effort set) is followed
	g, _, _ = FindGroup(GroupPrefix + "r")
	g.RenameMember("a/m", "a/m:high")
	if !slices.Equal(g.Relays, []Relay{{From: "b/big", To: "a/m:high"}}) {
		t.Fatalf("after the rename: %+v", g.Relays)
	}
	// a detector group, renamed, is followed, and can't go while it detects
	if err := SaveGroup(Group{Name: "Det", Members: []string{"a/m", "b/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "R", Members: []string{"b/big", "a/m"}, Relays: relays, Detector: "group/det"}); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("det", "judge"); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup(GroupPrefix + "r"); g.Detector != "group/judge" {
		t.Fatalf("detector %q after the rename", g.Detector)
	}
	if err := DeleteGroup("judge"); err == nil || !strings.Contains(err.Error(), "detector") {
		t.Fatalf("%v", err)
	}
	if err := SwitchGroup("judge", false); err == nil || !strings.Contains(err.Error(), "detector") {
		t.Fatalf("switched off: %v", err)
	}
	// without relays nothing is asked, and no detector is kept
	if err := SaveGroup(Group{Name: "R", Members: []string{"b/big", "a/m"}, Detector: "a/m", RelayFrom: 4}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup(GroupPrefix + "r"); g.Detector != "" || g.RelayFrom != 0 || g.Relayed() {
		t.Fatalf("kept without a relay: %+v", g)
	}
}
