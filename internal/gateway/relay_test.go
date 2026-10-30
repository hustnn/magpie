package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// relayGroup is ruled's group led by b/big, with a relay from it to a/small
// that d/det detects for once a turn has had 2 tool rounds.
func relayGroup(t *testing.T) (*Server, *ruleUp, *ruleUp, *clsUp) {
	t.Helper()
	s, a, b := ruled(t)
	d := &clsUp{answer: "0"}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "d", Name: "D", Key: "kd", Models: []string{"det"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("d", srv.URL+"/v1", []catalog.Model{{ID: "det", Context: 128000}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/r")
	g.Members = []string{"b/big", "a/small"}
	g.Relays = []provider.Relay{{From: "b/big", To: "a/small"}}
	g.Detector, g.RelayFrom = "d/det", 2
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return s, a, b, d
}

// toolStep is one tool call of a turn and what it returned.
type toolStep struct{ name, args, result string }

var (
	lsStep   = toolStep{"ls", `{}`, "file.txt"}
	editStep = toolStep{"Edit", `{"file_path":"cache.go","old_string":"a","new_string":"b"}`, "The file cache.go has been updated."}
	testStep = toolStep{"Bash", `{"command":"go test ./..."}`, "--- FAIL: TestEvict (0.00s)\n    cache_test.go:12: got 4, want 3\nFAIL"}
)

// relayChat is a Chat conversation: the user's words, then each toolStep a
// tool round of its own.
func relayChat(first string, steps ...toolStep) string {
	ms := []string{`{"role":"user","content":` + quote(first) + `}`}
	for i, st := range steps {
		id := fmt.Sprintf("c%d", i)
		ms = append(ms, `{"role":"assistant","content":null,"tool_calls":[{"id":"`+id+`","type":"function","function":{"name":`+quote(st.name)+`,"arguments":`+quote(st.args)+`}}]}`,
			`{"role":"tool","tool_call_id":"`+id+`","content":`+quote(st.result)+`}`)
	}
	return `{"model":"group/r","messages":[` + strings.Join(ms, ",") + `]}`
}

// postRelay is postOK for a group the detector asks about in the
// background: its own call goes through the gateway too, and may be
// traced after the agent's, so the route is the agent's latest.
func postRelay(t *testing.T, s *Server, session, body string) (string, Route) {
	t.Helper()
	code, out := postAs(t, s, session, body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	for i := len(s.trace.routes) - 1; i >= 0; i-- {
		if r := s.trace.routes[i]; r.Model == "group/r" {
			return out, *r
		}
	}
	t.Fatal("no route of the agent's")
	return "", Route{}
}

// relayIdle waits for the detector's asks out to end.
func relayIdle(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		relayTurns.Lock()
		busy := false
		for _, rt := range relayTurns.m {
			busy = busy || rt.busy
		}
		relayTurns.Unlock()
		if !busy {
			return
		}
	}
	t.Fatal("the detector's ask didn't end")
}

// A turn on the relay's From is handed on once the detector says it is
// ready: asked in the background as tool results come back, once the turn
// has had its rounds and written and run something; the request after
// its answer goes to To, the turn stays there, and the next message
// begins on From again.
func TestRelayHandsATurnOnOnce(t *testing.T) {
	s, _, _, d := relayGroup(t)
	d.set("1", 0)
	task := "add a test for the LRU cache's eviction"
	out, r := postRelay(t, s, "s1", relayChat(task))
	if !strings.Contains(out, "from kb") || r.Rule == nil || r.Rule.Relay != nil {
		t.Fatalf("turn start: %s %+v", out, r.Rule)
	}
	// a round, not the 2 the detector waits for
	out, r = postRelay(t, s, "s1", relayChat(task, lsStep))
	if !strings.Contains(out, "from kb") || r.Rule.Relay == nil || r.Rule.Relay.Moved || r.Rule.Relay.Why != relayRounds || r.Rule.Relay.Need != 2 || r.Rule.Relay.Rounds != 1 || d.n() != 0 {
		t.Fatalf("round 1: %s %+v", out, r.Rule.Relay)
	}
	// two rounds, but nothing written
	_, r = postRelay(t, s, "s1", relayChat(task, lsStep, lsStep))
	if r.Rule.Relay.Why != relayUnbuilt || d.n() != 0 {
		t.Fatalf("nothing written: %+v %d", r.Rule.Relay, d.n())
	}
	// written and run: the detector is asked, in the background
	out, r = postRelay(t, s, "s1", relayChat(task, lsStep, editStep, testStep))
	if !strings.Contains(out, "from kb") || !r.Rule.Relay.Asking || r.Rule.Relay.Asks != 1 {
		t.Fatalf("asked: %s %+v", out, r.Rule.Relay)
	}
	relayIdle(t)
	if d.n() != 1 {
		t.Fatalf("detector asked %d times", d.n())
	}
	asked := d.last()
	if !strings.Contains(asked, `"model":"det"`) || !strings.Contains(asked, "add a test for the LRU cache") ||
		!strings.Contains(asked, "Round 3:") || !strings.Contains(asked, "go test ./...") || !strings.Contains(asked, "got 4, want 3") {
		t.Fatalf("the detector was asked %s", asked)
	}
	// the request after its answer is handed on
	more := []toolStep{lsStep, editStep, testStep, editStep}
	out, r = postRelay(t, s, "s1", relayChat(task, more...))
	if !strings.Contains(out, "from ka") || !r.Rule.Relay.Moved || r.Rule.Relay.Round != 3 || r.Rule.Use != "a/small" || r.Rule.Held {
		t.Fatalf("moved: %s %+v %+v", out, r.Rule, r.Rule.Relay)
	}
	// and stays there, the detector asked no more
	more = append(more, testStep)
	out, r = postRelay(t, s, "s1", relayChat(task, more...))
	if !strings.Contains(out, "from ka") || !r.Rule.Relay.Held || !r.Rule.Held || r.Rule.Use != "a/small" {
		t.Fatalf("held: %s %+v %+v", out, r.Rule, r.Rule.Relay)
	}
	relayIdle(t)
	if d.n() != 1 {
		t.Fatalf("detector asked %d times after the turn moved", d.n())
	}
	// the next message begins on the member the group puts first
	next := `{"model":"group/r","messages":[{"role":"user","content":` + quote(task) + `},{"role":"assistant","content":"done"},{"role":"user","content":"now the other cache"}]}`
	out, r = postRelay(t, s, "s1", next)
	if !strings.Contains(out, "from kb") || r.Rule.Relay == nil || !r.Rule.Relay.Back || r.Rule.Use != "b/big" {
		t.Fatalf("next turn: %s %+v", out, r.Rule)
	}
	// and its tool rounds stay there, to be handed on again
	next = strings.TrimSuffix(next, "]}") + `,{"role":"assistant","content":null,"tool_calls":[{"id":"n1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"n1","content":"x"}]}`
	out, r = postRelay(t, s, "s1", next)
	if !strings.Contains(out, "from kb") || !r.Rule.Held || r.Rule.Relay == nil || r.Rule.Relay.Moved || r.Rule.Relay.Back {
		t.Fatalf("next turn's round: %s %+v %+v", out, r.Rule, r.Rule.Relay)
	}
}

// A detector that says not yet leaves the turn where it is, and is asked
// again only about rounds it wasn't shown.
func TestRelayNotYet(t *testing.T) {
	s, _, _, d := relayGroup(t)
	task := "refactor the sync engine"
	steps := []toolStep{editStep, testStep}
	postRelay(t, s, "s2", relayChat(task))
	postRelay(t, s, "s2", relayChat(task, steps...))
	relayIdle(t)
	out, r := postRelay(t, s, "s2", relayChat(task, steps...)) // the same rounds again: a retry
	relayIdle(t)
	if !strings.Contains(out, "from kb") || r.Rule.Relay.Moved || r.Rule.Relay.Why != relayNotYet || r.Rule.Relay.Seen != 2 || d.n() != 1 {
		t.Fatalf("not yet: %s %+v %d", out, r.Rule.Relay, d.n())
	}
	steps = append(steps, editStep)
	postRelay(t, s, "s2", relayChat(task, steps...))
	relayIdle(t)
	if d.n() != 2 {
		t.Fatalf("a new round asks again: %d", d.n())
	}
}

// A detector that fails leaves the turn where it is, and rests: the
// turns meanwhile don't ask it.
func TestRelayDetectorFails(t *testing.T) {
	s, _, _, d := relayGroup(t)
	d.set("1", 500)
	task := "fix the flaky test"
	steps := []toolStep{editStep, testStep}
	postRelay(t, s, "s3", relayChat(task))
	postRelay(t, s, "s3", relayChat(task, steps...))
	relayIdle(t)
	asked := d.n()
	steps = append(steps, editStep)
	out, r := postRelay(t, s, "s3", relayChat(task, steps...))
	if !strings.Contains(out, "from kb") || r.Rule.Relay.Moved || r.Rule.Relay.Why != relayResting || !strings.Contains(r.Rule.Relay.Error, "classifier down") || r.Rule.Relay.Asking {
		t.Fatalf("failed: %s %+v", out, r.Rule.Relay)
	}
	relayIdle(t)
	if asked == 0 || d.n() != asked {
		t.Fatalf("asked %d times while resting, %d before", d.n(), asked)
	}
	// an answer that is no 1 or 0 doesn't rest it
	relayTurns.Lock()
	relayTurns.rest, relayTurns.restWhy = map[string]time.Time{}, map[string]string{}
	relayTurns.Unlock()
	d.set("maybe", 0)
	steps = append(steps, testStep)
	postRelay(t, s, "s3", relayChat(task, steps...))
	relayIdle(t)
	steps = append(steps, editStep)
	_, r = postRelay(t, s, "s3", relayChat(task, steps...))
	relayIdle(t)
	if r.Rule.Relay.Why != relayFailed || !strings.Contains(r.Rule.Relay.Error, `not 1 or 0`) || !r.Rule.Relay.Asking || d.n() != asked+2 {
		t.Fatalf("odd answer: %+v %d, %d before", r.Rule.Relay, d.n(), asked)
	}
}

// To must take the conversation: one that has grown past 95% of To's
// context isn't handed on, and the detector isn't asked again.
func TestRelayKeepsATurnTooLongForTo(t *testing.T) {
	s, _, _, d := relayGroup(t)
	d.set("1", 0)
	task := long(61000) // a/small takes 64k
	steps := []toolStep{editStep, testStep}
	postRelay(t, s, "s4", relayChat(task))
	postRelay(t, s, "s4", relayChat(task, steps...))
	relayIdle(t)
	steps = append(steps, editStep)
	out, r := postRelay(t, s, "s4", relayChat(task, steps...))
	if !strings.Contains(out, "from kb") || r.Rule.Relay.Moved || r.Rule.Relay.Why != relayTooLong || r.Rule.Relay.Context != 64000 {
		t.Fatalf("too long: %s %+v", out, r.Rule.Relay)
	}
	steps = append(steps, testStep)
	postRelay(t, s, "s4", relayChat(task, steps...))
	relayIdle(t)
	if d.n() != 1 {
		t.Fatalf("asked %d times", d.n())
	}
}

// A turn handed on fails over to the member it was handed from while To
// can't answer, and goes back to To after.
func TestRelayFailsBackToFrom(t *testing.T) {
	s, a, _, d := relayGroup(t)
	d.set("1", 0)
	task := "port the parser"
	steps := []toolStep{editStep, testStep}
	postRelay(t, s, "s5", relayChat(task))
	postRelay(t, s, "s5", relayChat(task, steps...))
	relayIdle(t)
	steps = append(steps, editStep)
	if out, _ := postRelay(t, s, "s5", relayChat(task, steps...)); !strings.Contains(out, "from ka") {
		t.Fatalf("not handed on: %s", out)
	}
	a.mu.Lock()
	a.fail = 500
	a.mu.Unlock()
	steps = append(steps, testStep)
	out, r := postRelay(t, s, "s5", relayChat(task, steps...))
	if !strings.Contains(out, "from kb") || !r.Rule.Relay.Held {
		t.Fatalf("fail over: %s %+v", out, r.Rule.Relay)
	}
}

// A request magpie makes itself — the classifier's or the detector's own
// — asks no detector.
func TestRelayNotForRouterCalls(t *testing.T) {
	g := provider.Group{ID: "x", Relays: []provider.Relay{{From: "b/big", To: "a/small"}}, Detector: "d/det", RelayFrom: 1}
	req, err := parse(provider.Chat, []byte(relayChat("t", editStep, testStep)))
	if err != nil {
		t.Fatal(err)
	}
	fresh(t)
	turnRules.Lock()
	turnRules.m["k"] = turnRule{turn: 1, on: "b/big", at: time.Now()}
	turnRules.Unlock()
	hit := ruleFor("k", g, nil, req, "claude", nil, nil)
	if hit == nil || hit.Relay == nil || hit.Relay.Moved || hit.Relay.Why != relayNoDetector {
		t.Fatalf("%+v", hit)
	}
}

// The detector's prompt only grows from one ask to the next, so a cache
// of its start holds; past the budget the oldest rounds go.
func TestRelayPromptGrows(t *testing.T) {
	user := func(body []byte) string {
		var r struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.Unmarshal(body, &r); err != nil || len(r.Messages) != 2 {
			t.Fatalf("%v %s", err, body)
		}
		return r.Messages[1].Content
	}
	steps := func(n int) []relayStep {
		req, err := parse(provider.Chat, []byte(relayChat("the task", append([]toolStep{editStep}, slicesRepeat(testStep, n-1)...)...)))
		if err != nil {
			t.Fatal(err)
		}
		return relaySteps(req)
	}
	a, b := user(relayBody("d/det", "the task", steps(3))), user(relayBody("d/det", "the task", steps(4)))
	cut := strings.LastIndex(a, "\nIs the hard part done")
	if cut < 0 || !strings.HasPrefix(b, a[:cut]) {
		t.Fatalf("not a prefix:\n%s\n---\n%s", a, b)
	}
	defer func(was int) { relayBudget = was }(relayBudget)
	relayBudget = 2 * len([]rune(renderStep(1, steps(1)[0])))
	c := user(relayBody("d/det", "the task", steps(6)))
	if !strings.Contains(c, "left out)") || strings.Contains(c, "Round 1:") || !strings.Contains(c, "Round 6:") {
		t.Fatalf("over budget:\n%s", c)
	}
}

func slicesRepeat(st toolStep, n int) []toolStep {
	out := make([]toolStep, n)
	for i := range out {
		out[i] = st
	}
	return out
}

// What counts as built and tried: a write, then a command that ran.
func TestWroteThenRan(t *testing.T) {
	for _, c := range []struct {
		calls []relayCall
		want  bool
	}{
		{[]relayCall{{name: "Bash", args: `{"command":"go test"}`, answered: true}}, false},
		{[]relayCall{{name: "Edit", answered: true}}, false},
		{[]relayCall{{name: "Edit", answered: true}, {name: "Bash", args: `{"command":"go test"}`, answered: true}}, true},
		{[]relayCall{{name: "Edit", answered: true}, {name: "Bash", answered: false}}, false},
		{[]relayCall{{name: "Bash", args: `{"command":"cat > a.go <<EOF"}`, answered: true}, {name: "exec_command", answered: true}}, true},
		{[]relayCall{{name: "shell", args: `{"command":["apply_patch","*** Begin Patch"]}`, answered: true}, {name: "shell", answered: true}}, true},
		{[]relayCall{{name: "Read", answered: true}, {name: "Grep", answered: true}}, false},
	} {
		var steps []relayStep
		for _, call := range c.calls {
			steps = append(steps, relayStep{calls: []relayCall{call}})
		}
		if got := wroteThenRan(steps); got != c.want {
			t.Errorf("%+v: %v, want %v", c.calls, got, c.want)
		}
	}
}

func TestReadReady(t *testing.T) {
	for in, want := range map[string]bool{"1": true, "0": false, " 1.": true, "Answer: 0": false} {
		if got, err := readReady(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "yes", "2"} {
		if _, err := readReady(in); err == nil {
			t.Errorf("%q read", in)
		}
	}
}
