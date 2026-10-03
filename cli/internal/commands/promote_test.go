package commands

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// followedPromotion is the agent's side of a promotion that is started and
// polled: the record as each poll finds it, and when the agent is away.
type followedPromotion struct {
	// states are the record at the request that begins the promotion and at
	// every poll after it; the last one stays.
	states []api.Promotion
	// running says that the promotion was begun before the command ran.
	running bool
	// loseAnswer makes the request that begins the promotion arrive and its
	// answer not: the proxy answers 502 in the agent's place.
	loseAnswer bool
	// awayAt are the polls, counted from the first one after the promotion
	// began, that find the proxy answering for an agent that is restarting.
	awayAt map[int]int

	begun bool
	at    int
	polls int
}

func (p *followedPromotion) start(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Query().Get("wait") != "false":
		respondError(w, 400, api.Error{Code: api.CodeInvalidRequest, Message: "the test's agent is asked without wait=false"})
	case p.begun || p.running:
		respondError(w, 409, api.Error{Code: api.CodePromotionInProgress, Message: "a promotion is running on this server; follow it with: shipwick standby promote"})
	case p.loseAnswer:
		p.begun = true
		w.WriteHeader(http.StatusBadGateway)
	default:
		p.begun = true
		respond(w, 202, p.states[0])
	}
}

func (p *followedPromotion) poll(w http.ResponseWriter) {
	if !p.begun && !p.running {
		respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "this server was never promoted"})
		return
	}
	p.polls++
	if status, away := p.awayAt[p.polls]; away {
		w.WriteHeader(status)
		return
	}
	if p.at < len(p.states)-1 {
		p.at++
	}
	respond(w, 200, p.states[p.at])
}

// promotionStates is a promotion of db and web, as its record reads while it
// runs: begun, db up, and ended with what became of web.
func promotionStates(web api.PromotedApplication) []api.Promotion {
	records := []api.DNSRecord{{Hostname: "web.example.com", Type: "A", Value: "203.0.113.77"}}
	state := func(db, web api.PromotedApplication) api.Promotion {
		return api.Promotion{ID: 1, Status: api.PromotionRunning, StartedAt: fixedNow, Records: records,
			Applications: []api.PromotedApplication{db, web}}
	}
	done := state(api.PromotedApplication{Name: "db", Status: api.PromotedRunning}, web)
	ended := fixedNow.Add(time.Minute)
	done.CompletedAt, done.Status = &ended, api.PromotionSucceeded
	if web.Status == api.PromotedFailed {
		done.Status = api.PromotionFailed
	}
	return []api.Promotion{
		state(api.PromotedApplication{Name: "db", Status: api.PromotedPending}, api.PromotedApplication{Name: "web", Status: api.PromotedPending}),
		state(api.PromotedApplication{Name: "db", Status: api.PromotedStarting}, api.PromotedApplication{Name: "web", Status: api.PromotedPending}),
		state(api.PromotedApplication{Name: "db", Status: api.PromotedRunning}, api.PromotedApplication{Name: "web", Status: api.PromotedStarting}),
		state(api.PromotedApplication{Name: "db", Status: api.PromotedRunning}, api.PromotedApplication{Name: "web", Status: api.PromotedStarting}),
		done,
	}
}

func newPromotingAgent(t *testing.T, web api.PromotedApplication) *exportAgent {
	t.Helper()
	e := newExportAgent(t)
	e.standby = api.Standby{Applications: []api.StandbyApplication{
		{Name: "db", Version: "17", Hostnames: []string{}, ImportedAt: fixedNow},
		{Name: "web", Version: "1.4.2", Hostnames: []string{"web.example.com"}, ImportedAt: fixedNow},
	}}
	e.followed = &followedPromotion{states: promotionStates(web)}
	return e
}

func TestStandbyPromoteFollowsThePromotionAndSaysEachApplicationOnce(t *testing.T) {
	e := newPromotingAgent(t, api.PromotedApplication{Name: "web", Status: api.PromotedStarted, Message: "started, and not ready yet: connection refused. It is restarted until it is; watch it with: shipwick status web"})
	if _, _, err := e.run(t.TempDir(), "standby", "promote"); err == nil || !strings.Contains(err.Error(), "--yes") || e.promoted != 0 {
		t.Fatalf("a promotion without confirmation: %v", err)
	}
	out, errOut, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	all := out + errOut
	if err != nil || e.promoted != 1 {
		t.Fatalf("promote: %v, asked %d times\n%s", err, e.promoted, all)
	}
	for _, want := range []string{"db is running", "web: started, and not ready yet", "Change these DNS records", "web.example.com", "203.0.113.77"} {
		if strings.Count(all, want) != 1 {
			t.Errorf("output says %q %d times, want once:\n%s", want, strings.Count(all, want), all)
		}
	}
	if strings.Index(all, "db is running") > strings.Index(all, "web: started") {
		t.Errorf("the applications are not said in the order they came up:\n%s", all)
	}
}

func TestStandbyPromoteCarriesOnAcrossAnAgentThatIsAway(t *testing.T) {
	e := newPromotingAgent(t, api.PromotedApplication{Name: "web", Status: api.PromotedRunning})
	// The agent restarts after db came up: the proxy answers for it, then
	// nothing does, then it is back.
	e.followed.awayAt = map[int]int{2: http.StatusBadGateway, 3: http.StatusServiceUnavailable, 4: http.StatusGatewayTimeout}
	out, errOut, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	all := out + errOut
	if err != nil || e.promoted != 1 {
		t.Fatalf("promote across a restart: %v, asked %d times\n%s", err, e.promoted, all)
	}
	for _, want := range []string{"db is running", "web is running", "Change these DNS records"} {
		if strings.Count(all, want) != 1 {
			t.Errorf("output says %q %d times, want once:\n%s", want, strings.Count(all, want), all)
		}
	}
}

func TestStandbyPromoteGivesUpOnAnAgentThatStaysAwayAndSaysThePromotionGoesOn(t *testing.T) {
	e := newPromotingAgent(t, api.PromotedApplication{Name: "web", Status: api.PromotedRunning})
	e.followed.awayAt = map[int]int{}
	for poll := 2; poll < 2+maxPollFailures; poll++ {
		e.followed.awayAt[poll] = http.StatusBadGateway
	}
	_, _, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not answering") || !strings.Contains(err.Error(), "shipwick standby promote") {
		t.Fatalf("an agent that stays away: %v", err)
	}
}

func TestStandbyPromoteFindsThePromotionWhoseAnswerWasLost(t *testing.T) {
	e := newPromotingAgent(t, api.PromotedApplication{Name: "web", Status: api.PromotedRunning})
	e.followed.loseAnswer = true
	out, errOut, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	all := out + errOut
	if err != nil {
		t.Fatalf("promote: %v\n%s", err, all)
	}
	if e.promoted != 1 {
		t.Errorf("the promotion was asked for %d times, want once: the request had arrived", e.promoted)
	}
	if !strings.Contains(all, "db is running") || !strings.Contains(all, "web is running") {
		t.Errorf("output:\n%s", all)
	}
}

func TestStandbyPromoteFollowsAPromotionThatIsAlreadyRunning(t *testing.T) {
	e := newPromotingAgent(t, api.PromotedApplication{Name: "web", Status: api.PromotedRunning})
	e.followed.running = true
	// No --yes: nothing is begun, so nothing is to be confirmed.
	out, errOut, err := e.run(t.TempDir(), "standby", "promote")
	all := out + errOut
	if err != nil || e.promoted != 0 {
		t.Fatalf("promote while one runs: %v, asked %d times\n%s", err, e.promoted, all)
	}
	for _, want := range []string{"A promotion is running", "db is running", "web is running", "203.0.113.77"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
}

func TestStandbyPromoteFailsWhenAnApplicationCouldNotBeStarted(t *testing.T) {
	e := newPromotingAgent(t, api.PromotedApplication{Name: "web", Status: api.PromotedFailed, Message: "replica 1: container shipwick_web_1_1 no longer exists; deploy the application again"})
	out, errOut, err := e.run(t.TempDir(), "standby", "promote", "--yes")
	all := out + errOut
	if err != ErrReported {
		t.Fatalf("err = %v, want the failure reported\n%s", err, all)
	}
	for _, want := range []string{"db is running", "web could not be started: replica 1", "Change these DNS records"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
}

func TestStandbyShowsThePromotionThatRanLast(t *testing.T) {
	e := newExportAgent(t)
	states := promotionStates(api.PromotedApplication{Name: "web", Status: api.PromotedFailed, Message: "it is no longer deployed on this server"})
	e.standby = api.Standby{Applications: []api.StandbyApplication{}, Promotion: &states[len(states)-1]}
	out, _, err := e.run(t.TempDir(), "standby")
	if err != nil || !strings.Contains(out, "Promoted") || !strings.Contains(out, "web: failed: it is no longer deployed") {
		t.Errorf("standby after a promotion: %v\n%s", err, out)
	}
	e.standby.Promotion = &states[2]
	out, _, err = e.run(t.TempDir(), "standby")
	if err != nil || !strings.Contains(out, "A promotion is running") || !strings.Contains(out, "web: starting") {
		t.Errorf("standby during a promotion: %v\n%s", err, out)
	}
}
