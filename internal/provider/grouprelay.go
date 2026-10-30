package provider

// A relay (Group.Relays) hands a turn on, once, from the member it is on
// to another, cheaper one, when the group's detector (Group.Detector)
// judges the hard part done: an implementation or artifact there, run or
// checked, and what is left of the turn local — diagnosis, repair,
// validation, the final output (AgentBaton, arXiv 2610.35959: a capable
// model's progress lets an efficient one finish what it can't begin). The
// gateway asks the detector as the agent hands tool results back
// (gateway/relay.go); the turn moves at most once, and the user's next
// message begins on whichever member the rules put first again.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Relay hands a turn on from one of a group's members to another.
type Relay struct {
	From string `json:"from"` // the member a turn is on, as the group names it
	To   string `json:"to"`   // the member it is handed on to
}

// RelayFromDefault is how many tool rounds a turn has had before the
// detector is first asked (Group.RelayFrom 0): fewer have rarely built and
// run anything yet.
const RelayFromDefault = 6

// Relayed reports whether the group hands any turn on.
func (g Group) Relayed() bool { return len(g.Relays) > 0 }

// RelayOf is the relay that hands a turn on from member; false when none
// does.
func (g Group) RelayOf(member string) (Relay, bool) {
	i := slices.IndexFunc(g.Relays, func(r Relay) bool { return r.From == member })
	if i < 0 {
		return Relay{}, false
	}
	return g.Relays[i], true
}

// RelayRounds is how many tool rounds a turn has before the detector is
// first asked.
func (g Group) RelayRounds() int {
	if g.RelayFrom > 0 {
		return g.RelayFrom
	}
	return RelayFromDefault
}

// MapRelays has each relay's members go by what f makes of them: a
// member renamed, cleaned or taken out (""), a relay with either end
// taken out going with it.
func (g *Group) MapRelays(f func(string) string) {
	var out []Relay
	for _, r := range g.Relays {
		r.From, r.To = f(r.From), f(r.To)
		if r.From != "" && r.To != "" {
			out = append(out, r)
		}
	}
	g.Relays = out
}

// cleanRelays checks a group's relays against its members: each end one
// of them, a member handing on to another, and each member the start of
// one relay at most. A member a turn is handed on to hands nothing on
// itself, so a turn moves once (no chain, and so no ring).
func cleanRelays(relays []Relay, members []string) ([]Relay, error) {
	var out []Relay
	for i, r := range relays {
		n := i + 1
		r.From, r.To = strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		switch {
		case r.From == "" || r.To == "":
			return nil, fmt.Errorf("relay %d: it needs the model a turn is on and the one it is handed on to", n)
		case !slices.Contains(members, r.From):
			return nil, fmt.Errorf("relay %d: %s is not in the group", n, r.From)
		case !slices.Contains(members, r.To):
			return nil, fmt.Errorf("relay %d: %s is not in the group", n, r.To)
		case r.From == r.To:
			return nil, fmt.Errorf("relay %d: %s can't hand a turn on to itself", n, r.From)
		case slices.ContainsFunc(out, func(o Relay) bool { return o.From == r.From }):
			return nil, fmt.Errorf("relay %d: %s hands its turns on already: one relay from each model", n, r.From)
		}
		out = append(out, r)
	}
	for _, r := range out {
		if slices.ContainsFunc(out, func(o Relay) bool { return o.From == r.To }) {
			return nil, fmt.Errorf("%s is handed turns on to, so it can't hand them on itself: a turn moves once", r.To)
		}
	}
	return out, nil
}

// cleanDetector checks the model a group with relays asks whether a turn
// is ready to be handed on: one magpie has that answers in words — a
// decision model (Jev) answers intents and efforts only — or a group of
// such models, never this one. A group without relays asks nothing, and
// keeps none.
func cleanDetector(g *Group, entries []Entry) error {
	g.Detector = strings.TrimPrefix(strings.TrimSpace(g.Detector), "magpie/")
	g.RelayFrom = max(g.RelayFrom, 0)
	if !g.Relayed() {
		g.Detector, g.RelayFrom = "", 0
		return nil
	}
	if g.Detector == "" {
		return errors.New("a relay needs the group's detector: the model that tells when a turn is ready to be handed on")
	}
	if g.Detector == GroupPrefix+g.ID {
		return fmt.Errorf("%s can't be its own detector: asking it would ask it again", g.Name)
	}
	if gid, ok := strings.CutPrefix(g.Detector, GroupPrefix); ok {
		d, ok := groupOf(groupsIn(entries), gid)
		switch {
		case !ok:
			return fmt.Errorf("magpie has no group %q to detect with", gid)
		case d.Disabled:
			return fmt.Errorf("%s is switched off: switch it on to detect with it", d.Name)
		case d.Decides():
			return fmt.Errorf("%s is a group of decision models, which answer intents and efforts only: a detector is asked in words", d.Name)
		}
		return nil
	}
	p, m, ok := Resolve(g.Detector)
	if !ok {
		return fmt.Errorf("magpie knows no model %q to detect with", g.Detector)
	}
	if p.DecidesModel(m) {
		return fmt.Errorf("%s is a decision model, which answers intents and efforts only: a detector is asked in words", g.Detector)
	}
	return nil
}
