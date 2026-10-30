package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// A group's relays from the terminal: magpie group relay add|rm|detector …

const relayUsage = `usage:
  magpie group relay <group>              the group's relays and its detector
  magpie group relay add <group> <from> <to> [detector=<model>] [rounds=<n>]
                                          a relay: a turn on <from>, one of the group's models, is handed
                                          on to <to>, another of them, once the detector says the hard part
                                          is done — an implementation there, run or checked, and what is
                                          left local: diagnosis, repair, validation, the final output.
                                          The turn moves once and stays on <to>; your next message begins
                                          where the rules put it again. A model in the group at an effort
                                          of its own is named with it (lmstudio/qwen3.8-27b:xhigh)
  magpie group relay rm <group> <n>       remove relay n
  magpie group relay detector <group> <model>
                                          the model asked whether a turn is ready to hand on: any model
                                          magpie has that answers in words (not a decision model such as
                                          Jev), or group/<id>, its models tried in turn
  magpie group relay rounds <group> <n>   how many tool rounds a turn has before the detector is first
                                          asked (6 by default)

  The detector is asked in the background as the agent hands tool results back, once a
  turn has written something and run something since, at most once at a time: no request
  waits for it. Its calls show in the usage as magpie's own. If it fails or is slow, the
  turn stays where it is. A model a turn is handed on to hands nothing on itself.

  e.g. magpie group relay add coder kimi/kimi-k3 lmstudio/qwen3.8-27b:xhigh detector=deepseek/deepseek-v4-flash
       magpie group relay detector coder ollama/qwen3.8-27b`

// relayCmd: magpie group relay …
func relayCmd(args []string) error {
	if len(args) == 0 || slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		fmt.Println(relayUsage)
		return nil
	}
	verb, rest := args[0], args[1:]
	if len(rest) == 0 {
		// magpie group relay <group>: the group, its relays with it
		if g, err := findGroup(verb); err == nil {
			if !g.Relayed() {
				fmt.Println(muted.Render("  " + g.ID + " hands no turn on · magpie group relay add " + g.ID + " <from> <to> detector=<model>"))
				return nil
			}
			return showGroup(g)
		}
		return fmt.Errorf("%s", relayUsage)
	}
	g, err := findGroup(rest[0])
	if err != nil {
		return err
	}
	if g.Hidden {
		return fmt.Errorf("%s was removed: magpie group restore %s brings it back first", g.ID, g.ID)
	}
	switch verb {
	case "add", "new":
		var ends []string
		for _, w := range rest[1:] {
			k, v, kv := strings.Cut(w, "=")
			switch {
			case !kv:
				ends = append(ends, w)
			case k == "detector" || k == "detect" || k == "by":
				g.Detector = v
			case k == "rounds" || k == "after":
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return fmt.Errorf("rounds is a number of tool rounds, 1 or more, not %q", v)
				}
				g.RelayFrom = n
			default:
				return fmt.Errorf("magpie group relay add takes no %q\n\n%s", k, relayUsage)
			}
		}
		if len(ends) != 2 {
			return fmt.Errorf("magpie group relay add <group> <from> <to> [detector=<model>]")
		}
		from, err := groupMember(g, ends[0])
		if err != nil {
			return err
		}
		to, err := groupMember(g, ends[1])
		if err != nil {
			return err
		}
		g.Relays = append(g.Relays, provider.Relay{From: from, To: to})
	case "rm", "remove", "delete":
		if len(rest) != 2 {
			return fmt.Errorf("magpie group relay rm <group> <n>")
		}
		n, err := strconv.Atoi(rest[1])
		if err != nil || n < 1 || n > len(g.Relays) {
			if len(g.Relays) == 0 {
				return fmt.Errorf("%s has no relays", g.ID)
			}
			return fmt.Errorf("relay %s: %s has relays 1 to %d", rest[1], g.ID, len(g.Relays))
		}
		g.Relays = slices.Delete(g.Relays, n-1, n)
	case "detector", "detect":
		if len(rest) != 2 {
			return fmt.Errorf("magpie group relay detector <group> <model>")
		}
		if !g.Relayed() {
			return fmt.Errorf("%s has no relay to detect for", g.ID)
		}
		g.Detector = rest[1]
	case "rounds", "after":
		if len(rest) != 2 {
			return fmt.Errorf("magpie group relay rounds <group> <n>")
		}
		n, err := strconv.Atoi(rest[1])
		if err != nil || n < 1 {
			return fmt.Errorf("rounds is a number of tool rounds, 1 or more, not %q", rest[1])
		}
		g.RelayFrom = n
	default:
		return fmt.Errorf("magpie group relay has no %q\n\n%s", verb, relayUsage)
	}
	if err := provider.SaveGroup(g); err != nil {
		return err
	}
	g, err = findGroup(g.ID)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "saved", bold.Render(g.Name))
	return showGroup(g)
}

// relayLine is a relay as magpie group shows it.
func relayLine(r provider.Relay) string {
	return r.From + muted.Render(" → when ready → ") + r.To
}
