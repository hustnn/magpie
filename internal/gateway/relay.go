package gateway

// A group's relays (provider.Relay) hand a turn on, once, from the member
// it is on to another, when the group's detector judges its hard part
// done (AgentBaton, arXiv 2610.35959). The detector is asked as the agent
// hands tool results back, in the background: no request waits for it,
// and what it says moves the turn at the agent's next request. It is
// shown the turn's task and its tool calls with what they returned — not
// the model's reasoning or its words — and answers 1 or 0. It isn't asked
// until the turn has had the group's rounds (Group.RelayRounds) and has
// written something and run something since: before that there is
// nothing to hand on. What fails or is slow leaves the turn where it is.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// relayAsker asks model the Chat request body and returns its answer's
// text: through the gateway, as magpie's own call.
type relayAsker func(model string, body []byte) (string, error)

var (
	relayWait   = 30 * time.Second // the detector's time to answer, its provider's failover included
	relayRest   = 30 * time.Second // after it fails, before it is asked again
	relayAsks   = 30               // a turn's asks at most
	relayBudget = 60000            // runes of rounds the detector is shown; past it, the oldest go
)

// The parts of a round the detector is shown, in runes: a tool call's
// arguments, and a result's head and tail.
const (
	relayArgs      = 300
	relayHead      = 600
	relayTail      = 400
	relayTaskChars = 4000
)

const relayPrompt = "You watch a coding agent working on a task, round by round, and judge whether its hard part is done, so that a smaller model can finish it. " +
	"Answer 1 only when all of these hold: " +
	"(1) a substantive implementation, runnable prototype or artifact the task asks for exists — investigation, a plan or code never run is not enough; " +
	"(2) it has been run, tested, compiled or checked, and the agent saw the result — a failing check counts when it points at a specific problem, an environment failure that kept it from running does not; " +
	"(3) what is left is local: diagnosing, fixing, hardening, validating or preparing the final output, not choosing the approach or writing the main solution. " +
	"Every check passing is not needed. Otherwise answer 0. Answer with the number only."

// Relayed is what the trace tells of a group's relay for a request.
type Relayed struct {
	From string `json:"from"`         // the member the turn is handed on from
	To   string `json:"to"`           // and to
	By   string `json:"by,omitempty"` // the detector
	// Moved: this request is the one the turn moved on; Held: an earlier
	// one did, and the turn stays on To
	Moved bool `json:"moved,omitempty"`
	Held  bool `json:"held,omitempty"`
	// Back: the turn before was handed on, and no rule decides this one:
	// it begins on From again, over the conversation's stay on To
	Back bool `json:"back,omitempty"`
	// Round: the turn's tool rounds when the detector said it was ready
	Round  int  `json:"round,omitempty"`
	Rounds int  `json:"rounds"`           // the turn's tool rounds now
	Asks   int  `json:"asks,omitempty"`   // how often the detector was asked in the turn
	Ms     int  `json:"ms,omitempty"`     // how long its answer took
	Asking bool `json:"asking,omitempty"` // an ask is out, begun with this request or before
	// Why: why the turn wasn't handed on with this request, a word the
	// Routing page says in the user's language: relayRounds,
	// relayUnbuilt, relayNotYet, relayFailed, relayResting, relayTooLong,
	// relayMostAsks or relayNoDetector
	Why string `json:"why,omitempty"`
	// Need: the rounds the detector waits for (relayRounds); Seen: the
	// rounds its last answer was about (relayNotYet); Error: what its
	// call said (relayFailed, relayResting); Context: To's (relayTooLong)
	Need    int    `json:"need,omitempty"`
	Seen    int    `json:"seen,omitempty"`
	Error   string `json:"error,omitempty"`
	Context int    `json:"context,omitempty"`
}

// The reasons a turn isn't handed on yet (Relayed.Why).
const (
	relayRounds     = "rounds"     // it hasn't had the rounds the detector waits for
	relayUnbuilt    = "unbuilt"    // it hasn't written something and run something since
	relayNotYet     = "notyet"     // the detector said its hard part isn't done
	relayFailed     = "failed"     // the detector couldn't say
	relayResting    = "resting"    // the detector failed a moment ago and rests
	relayTooLong    = "toolong"    // To can't take a conversation this long
	relayMostAsks   = "asks"       // the detector was asked as often as a turn asks it
	relayNoDetector = "nodetector" // there is nothing to ask it with
)

// relayTurn is a conversation's turn as its relay has asked about it.
type relayTurn struct {
	turn  int
	asks  int
	asked int  // the rounds the last ask was shown
	busy  bool // an ask is out
	ready bool
	round int // the rounds when it said ready
	ms    int
	// what it said last (relayNotYet, about seen rounds), or that it
	// couldn't (relayFailed, err)
	why  string
	seen int
	err  string
	// why it isn't asked again this turn (relayTooLong, relayMostAsks),
	// with To's context
	stop    string
	context int
	at      time.Time
}

var relayTurns = struct {
	sync.Mutex
	m       map[string]*relayTurn // ruleKey → its turn
	rest    map[string]time.Time  // detector model → when it failed
	restWhy map[string]string     // and what it said
}{m: map[string]*relayTurn{}, rest: map[string]time.Time{}, restWhy: map[string]string{}}

// relayTurnOf is the conversation key's relay record for turn: a new one
// when it was another turn's. Held under relayTurns.
func relayTurnOf(key string, turn int, now time.Time) *relayTurn {
	rt, ok := relayTurns.m[key]
	if !ok || rt.turn != turn {
		rt = &relayTurn{turn: turn}
		relayTurns.m[key] = rt
		if len(relayTurns.m) > 4096 {
			for k, o := range relayTurns.m {
				if now.Sub(o.at) > stickKeep && !o.busy {
					delete(relayTurns.m, k)
				}
			}
		}
	}
	rt.at = now
	return rt
}

// relayFor is what a request within a turn on a relay's From makes of the
// relay: the turn handed on to To with this request, when the detector
// said it is ready since the request before (moved); else the trace's
// account of it, an ask begun in the background when one is due. tokens
// is how long the conversation is now, for To to take it. Held under
// turnRules; it takes relayTurns.
func relayFor(key string, g provider.Group, r provider.Relay, ctx map[string]int, req *Request, turn, tokens int, detect relayAsker) (rel *Relayed, moved bool) {
	now := ruleClock()
	steps := relaySteps(req)
	rel = &Relayed{From: r.From, To: r.To, By: g.Detector, Rounds: len(steps)}
	relayTurns.Lock()
	defer relayTurns.Unlock()
	rt := relayTurnOf(key, turn, now)
	rel.Asks, rel.Ms = rt.asks, rt.ms
	if rt.ready {
		rel.Round = rt.round
		if c := ctx[r.To]; c > 0 && tokens >= c*95/100 {
			// the conversation only grows: To won't take it later either
			rt.stop, rt.context, rt.ready = relayTooLong, c, false
		} else {
			rel.Moved = true
			return rel, true
		}
	}
	switch {
	case rt.stop != "":
		rel.Why, rel.Context = rt.stop, rt.context
		return rel, false
	case detect == nil:
		rel.Why = relayNoDetector
		return rel, false
	case len(steps) < g.RelayRounds():
		rel.Why, rel.Need = relayRounds, g.RelayRounds()
		return rel, false
	case !wroteThenRan(steps):
		rel.Why = relayUnbuilt
		return rel, false
	}
	rel.Why, rel.Seen, rel.Error = rt.why, rt.seen, rt.err
	switch {
	case rt.busy:
		rel.Asking = true
		return rel, false
	case rt.asked >= len(steps):
		return rel, false // asked about these rounds already
	case rt.asks >= relayAsks:
		rt.stop = relayMostAsks
		rel.Why = rt.stop
		return rel, false
	}
	if at, ok := relayTurns.rest[g.Detector]; ok && now.Sub(at) < relayRest {
		rel.Why, rel.Error = relayResting, relayTurns.restWhy[g.Detector]
		return rel, false
	}
	rt.busy, rt.asks, rt.asked = true, rt.asks+1, len(steps)
	rel.Asks, rel.Asking = rt.asks, true
	body := relayBody(g.Detector, userTask(req), steps)
	go relayAsk(rt, g.Detector, len(steps), body, detect)
	return rel, false
}

// relayAsk asks the detector about the turn's first rounds and keeps what
// it says in rt.
func relayAsk(rt *relayTurn, model string, rounds int, body []byte, detect relayAsker) {
	t0 := time.Now()
	var answer string
	err := fmt.Errorf("the detector did not finish")
	defer func() {
		// in the background, a panic would end the gateway: it is the
		// ask's failure instead
		if p := recover(); p != nil {
			err = fmt.Errorf("the detector's call panicked: %v", p)
		}
		ready, rerr := false, err
		if err == nil {
			ready, rerr = readReady(answer)
		}
		relayTurns.Lock()
		defer relayTurns.Unlock()
		rt.busy, rt.ms = false, int(time.Since(t0).Milliseconds())
		switch {
		case rerr != nil:
			rt.why, rt.err = relayFailed, rerr.Error()
			if _, odd := rerr.(answerError); !odd {
				if _, was := relayTurns.rest[model]; !was {
					log.Printf("routing: the relay detector %s couldn't say: %v", model, rerr)
				}
				relayTurns.rest[model], relayTurns.restWhy[model] = time.Now(), rerr.Error()
			}
		case ready:
			delete(relayTurns.rest, model)
			rt.ready, rt.round, rt.why, rt.err = true, rounds, "", ""
		default:
			delete(relayTurns.rest, model)
			rt.why, rt.seen, rt.err = relayNotYet, rounds, ""
		}
	}()
	answer, err = detect(model, body)
}

// readReady is what a detector's answer says: its first number, 1 for
// ready and 0 for not yet.
func readReady(answer string) (bool, error) {
	switch firstNumber.FindString(answer) {
	case "1":
		return true, nil
	case "0":
		return false, nil
	}
	a := []rune(strings.TrimSpace(answer))
	if len(a) > 80 {
		a = append(a[:80], '…')
	}
	return false, answerError{fmt.Errorf("it answered %q, not 1 or 0", string(a))}
}

// relayStep is one round of the agent's turn: the tool calls the model
// made in one reply, each with what it returned.
type relayStep struct {
	calls []relayCall
}

type relayCall struct {
	name, args string
	cmd        string // the command a shell tool was given, as written
	result     string
	isError    bool
	answered   bool
}

// relaySteps are the rounds of the turn the request is in: each reply of
// the model's with tool calls since the user's message that began the
// turn, with the results the agent handed back.
func relaySteps(req *Request) []relayStep {
	at, _ := lastSaid(req, len(req.Messages))
	results := map[string]Part{}
	for _, m := range req.Messages[at+1:] {
		for _, p := range m.Parts {
			if p.Kind == ToolResult {
				results[p.CallID] = p
			}
		}
	}
	var out []relayStep
	for _, m := range req.Messages[at+1:] {
		if m.Role != "assistant" {
			continue
		}
		var st relayStep
		for _, p := range m.Parts {
			if p.Kind != ToolCall {
				continue
			}
			c := relayCall{name: p.Name, args: compactArgs(p.Args), cmd: commandOf(p.Args)}
			if res, ok := results[p.ID]; ok {
				c.result, c.isError, c.answered = res.Text, res.IsError, true
			}
			st.calls = append(st.calls, c)
		}
		if len(st.calls) > 0 {
			out = append(out, st)
		}
	}
	return out
}

// compactArgs is a tool call's arguments on one line.
// <, > and & are kept as written, not escaped as json.Marshal escapes
// them for HTML: a command's "cat > a.py <<EOF" is read as that, by the
// detector and by wroteThenRan.
func compactArgs(args json.RawMessage) string {
	var v any
	if json.Unmarshal(args, &v) != nil {
		return strings.Join(strings.Fields(string(args)), " ")
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// commandOf is the command a shell tool call runs, as written: Claude
// Code's {"command": "…"}, Codex's {"command": ["bash", "-lc", "…"]} or
// {"cmd": …}; "" for a call with none.
func commandOf(args json.RawMessage) string {
	var v map[string]any
	if json.Unmarshal(args, &v) != nil {
		return ""
	}
	for _, k := range []string{"command", "cmd"} {
		switch c := v[k].(type) {
		case string:
			return c
		case []any:
			var parts []string
			for _, p := range c {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

// writeTools are tools, by name, that write files: Claude Code's, Codex's
// and other agents' editing tools.
var writeTools = map[string]bool{
	"edit": true, "multiedit": true, "write": true, "notebookedit": true, "apply_patch": true,
	"str_replace_editor": true, "str_replace_based_edit_tool": true, "create_file": true,
	"write_file": true, "edit_file": true, "replace": true, "insert": true, "patch": true,
	"write_to_file": true, "replace_in_file": true,
}

// shellTools are tools, by name, that run a command.
var shellTools = map[string]bool{
	"bash": true, "shell": true, "exec_command": true, "run_terminal_cmd": true,
	"run_shell_command": true, "execute_command": true, "run_command": true, "local_shell": true,
	"terminal": true, "exec": true, "command": true, "powershell": true,
}

// shellWrites are what a shell command writes a file with: a redirect, a
// heredoc (cat <<, python - << with code that writes), an editor in place
// or a patch. A command they take for a write that isn't one only has the
// detector asked sooner.
var shellWrites = []string{"apply_patch", "> ", ">>", "<<", "tee ", "sed -i", "git apply", "patch -", ".write(", "write_text("}

// wroteThenRan reports whether the turn wrote something — an edit, or a
// command that writes — and ran a command after it whose result came
// back: there is something built, and it was tried.
func wroteThenRan(steps []relayStep) bool {
	wrote := false
	for _, st := range steps {
		for _, c := range st.calls {
			name := strings.ToLower(c.name)
			shell := shellTools[name] || strings.Contains(name, "test")
			switch {
			case writeTools[name]:
				wrote = true
			case shell && wrote && c.answered:
				return true
			case shell && containsAny(c.cmd+"\n"+c.args, shellWrites):
				wrote = true
			}
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// userTask is the user's message that began the turn, its middle left
// out when long.
func userTask(req *Request) string {
	_, text := lastSaid(req, len(req.Messages))
	return cutMiddle(text, relayTaskChars*3/4, relayTaskChars/4)
}

// relayBody is the Chat request asking model whether the turn is ready to
// hand on: the task and its rounds, in one user message that only grows
// from one ask to the next, so a detector that caches the start of a
// prompt reads the rounds it was shown before from its cache. Past
// relayBudget the oldest rounds go.
func relayBody(model, task string, steps []relayStep) []byte {
	rounds := make([]string, len(steps))
	total := 0
	for i, st := range steps {
		rounds[i] = renderStep(i+1, st)
		total += len([]rune(rounds[i]))
	}
	from := 0
	for total > relayBudget && from < len(rounds)-1 {
		total -= len([]rune(rounds[from]))
		from++
	}
	var b strings.Builder
	b.WriteString("Task:\n<task>\n" + task + "\n</task>\n\nRounds so far:\n")
	if from > 0 {
		fmt.Fprintf(&b, "(rounds 1 to %d left out)\n", from)
	}
	for _, r := range rounds[from:] {
		b.WriteString(r)
	}
	b.WriteString("\nIs the hard part done (1) or not yet (0)?")
	req := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": relayPrompt},
			{"role": "user", "content": b.String()},
		},
		"stream":      false,
		"temperature": 0,
		"max_tokens":  2048,
	}
	if e := classifyEffort(model); e != "" {
		req["reasoning_effort"] = e
	}
	body, _ := json.Marshal(req)
	return body
}

// renderStep is round n as the detector reads it.
func renderStep(n int, st relayStep) string {
	var b strings.Builder
	b.WriteString("Round " + strconv.Itoa(n) + ":\n")
	for _, c := range st.calls {
		b.WriteString("- " + c.name + " " + cutEnd(c.args, relayArgs) + "\n")
		switch {
		case !c.answered:
			b.WriteString("  → (no result)\n")
		default:
			out := strings.TrimSpace(cutMiddle(c.result, relayHead, relayTail))
			if c.isError {
				out = "error: " + out
			}
			b.WriteString("  → " + strings.ReplaceAll(out, "\n", "\n    ") + "\n")
		}
	}
	return b.String()
}

// cutEnd is s, at most n runes of it.
func cutEnd(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// askRelay is the detector's call through the gateway, as the
// classifier's is (askChat), given relayWait.
func (s *Server) askRelay(model string, body []byte) (string, error) {
	return s.askChatWithin(model, body, relayWait)
}

// memberOn is the group's member a candidate is of — the model, or the
// group in the group it is in — as ms has them; "" for none.
func memberOn(c candidate, ms []provider.Member) string {
	for _, m := range ms {
		if ofMember(c, m) {
			return m.ID
		}
	}
	return ""
}
