package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calinrus-dev/semadesk/internal/model"
)

type cancellableEngine struct {
	fakeEngine
	started chan struct{}
}

func (e *cancellableEngine) Act(ctx context.Context, _ model.Action, _ model.Node) error {
	close(e.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestAgentPauseCancelsOngoingAction(t *testing.T) {
	s, _, _ := testServer(t)
	e := &cancellableEngine{started: make(chan struct{})}
	s.engine = e
	finished := make(chan error, 1)
	go func() { finished <- s.act(context.Background(), model.Action{Action: "press_key", Key: "Return"}) }()
	select {
	case <-e.started:
	case <-time.After(2 * time.Second):
		t.Fatal("action did not start")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/control", strings.NewReader(`{"paused":true}`))
	w := httptest.NewRecorder()
	s.pauseHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("pause returned %d: %s", w.Code, w.Body)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("action did not abort")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause failed to cancel the pending action")
	}
	if err := s.act(context.Background(), model.Action{Action: "press_key", Key: "Return"}); err == nil {
		t.Fatal("agent action bypassed pause")
	}
}

func TestAgentStatusSurvivesViewerReconnect(t *testing.T) {
	_, _, ts := testServer(t)
	ws := connectWS(t, ts, "observer-status")
	readObject(t, ws)
	res := request(t, ts, "POST", "/api/agent-status?token=test-secret", `{"phase":"working","label":"Checking export"}`, "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status returned %d", res.StatusCode)
	}
	closeResponse(res)
	event := readObject(t, ws)
	if event["type"] != "agent_status" || event["status"].(map[string]any)["label"] != "Checking export" {
		t.Fatal(event)
	}
	newObserver := connectWS(t, ts, "observer-reconnect")
	snapshot := readObject(t, newObserver)
	if snapshot["agent_status"].(map[string]any)["phase"] != "working" {
		t.Fatal(snapshot)
	}
}

func TestActionRejectsChangedNameWithStableID(t *testing.T) {
	s, e, ts := testServer(t)
	e.mu.Lock()
	e.nodes = []model.Node{{ID: "square-e4", Role: "button", Name: "e4 white pawn", Bounds: model.Rect{Width: 30, Height: 30}}}
	e.mu.Unlock()
	res := request(t, ts, "POST", "/api/act?token=test-secret", `{"action":"click","node":"square-e4","expect_name":"e4 empty"}`, "", "")
	defer res.Body.Close()
	var result struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusConflict || result.Code != "stale_observation" {
		t.Fatalf("unexpected result: %d %+v", res.StatusCode, result)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.actions != 0 || s.snapshot.Revision < 2 {
		t.Fatal("stale click ran or current scene was not published")
	}
}
