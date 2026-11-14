package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/gorilla/websocket"
)

type fakeEngine struct {
	mu      sync.Mutex
	nodes   []model.Node
	actions int
	changes chan struct{}
}

func (e *fakeEngine) Snapshot(context.Context) ([]model.Node, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.Node{}, e.nodes...), nil
}
func (e *fakeEngine) Changes() <-chan struct{} { return e.changes }
func (e *fakeEngine) Act(context.Context, model.Action, model.Node) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.actions++
	return nil
}

type fakeDesktop struct {
	mu     sync.Mutex
	text   string
	events chan struct{}
}

func (d *fakeDesktop) Size() (int, int)        { return 800, 600 }
func (d *fakeDesktop) Events() <-chan struct{} { return d.events }
func (d *fakeDesktop) SetClipboard(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.text = text
	return nil
}
func (d *fakeDesktop) Clipboard() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.text, nil
}

func testServer(t *testing.T) (*Server, *fakeEngine, *httptest.Server) {
	t.Helper()
	return testServerWithSocket(t, filepath.Join(t.TempDir(), "rfb.sock"))
}

func testServerWithSocket(t *testing.T, socket string) (*Server, *fakeEngine, *httptest.Server) {
	t.Helper()
	e := &fakeEngine{changes: make(chan struct{}, 1)}
	d := &fakeDesktop{events: make(chan struct{}, 1)}
	s, err := New(Config{Address: "127.0.0.1:0", Token: "test-secret", RFBSocket: socket, Width: 800, Height: 600}, e, d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(s.handler())
	s.host = ts.Listener.Addr().String()
	s.refresh()
	ts.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
		ts.Close()
	})
	return s, e, ts
}
func request(t *testing.T, ts *httptest.Server, method, path, body, origin, host string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if host != "" {
		r.Host = host
	}
	res, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func closeResponse(r *http.Response) { _, _ = io.Copy(io.Discard, r.Body); _ = r.Body.Close() }
func connectWS(t *testing.T, ts *httptest.Server, id string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws?token=test-secret&client="+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}
func readObject(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var value map[string]any
	if err := ws.ReadJSON(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func sendObject(t *testing.T, ws *websocket.Conn, value any) {
	t.Helper()
	if err := ws.WriteJSON(value); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationHostOriginAndAssetCookie(t *testing.T) {
	_, _, ts := testServer(t)
	cases := []struct {
		path, origin, host string
		status             int
	}{
		{"/api/status", "", "", 401},
		{"/api/status?token=wrong", "", "", 401},
		{"/api/status?token=test-secret", "http://evil.invalid", "", 403},
		{"/api/status?token=test-secret", "", "evil.invalid", 403},
		{"/api/status?token=test-secret", "", "", 200},
	}
	for _, c := range cases {
		r := request(t, ts, "GET", c.path, "", c.origin, c.host)
		if r.StatusCode != c.status {
			t.Errorf("%s: got %d want %d", c.path, r.StatusCode, c.status)
		}
		if r.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Error("unexpected CORS grant")
		}
		closeResponse(r)
	}
	r := request(t, ts, "GET", "/?token=test-secret", "", "", "")
	if r.StatusCode != 200 {
		t.Fatalf("index: %d", r.StatusCode)
	}
	cookies := r.Cookies()
	closeResponse(r)
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie lacks restrictions")
	}
	req, _ := http.NewRequest("GET", ts.URL+"/novnc/core/rfb.js", nil)
	req.AddCookie(cookies[0])
	r, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResponse(r)
	if r.StatusCode != 200 {
		t.Errorf("embedded module authentication failed: %d", r.StatusCode)
	}
}

func TestDiffRevisionAndResnapshot(t *testing.T) {
	s, e, ts := testServer(t)
	ws := connectWS(t, ts, "client-diff")
	first := readObject(t, ws)
	if first["type"] != "snapshot" {
		t.Fatal(first)
	}
	base := first["revision"].(float64)
	e.mu.Lock()
	e.nodes = []model.Node{{ID: "button1", Role: "button", Name: "Save", Bounds: model.Rect{X: 5, Y: 5, Width: 50, Height: 20}}}
	e.mu.Unlock()
	s.refresh()
	update := readObject(t, ws)
	if update["type"] != "diff" || update["base"] != base || update["revision"] != base+1 {
		t.Fatalf("invalid diff: %#v", update)
	}
	up := update["upsert"].([]any)
	if len(up) != 1 {
		t.Fatal(update)
	}
	e.mu.Lock()
	e.nodes = nil
	e.mu.Unlock()
	s.refresh()
	update = readObject(t, ws)
	if update["base"] != base+1 || update["revision"] != base+2 || len(update["remove"].([]any)) != 1 {
		t.Fatal(update)
	}
	sendObject(t, ws, map[string]any{"type": "snapshot"})
	snapshot := readObject(t, ws)
	if snapshot["revision"] != base+2 || len(snapshot["nodes"].([]any)) != 0 {
		t.Fatal(snapshot)
	}
}

func TestTakeoverPausesAgentAndHasPrivateLease(t *testing.T) {
	s, e, ts := testServer(t)
	owner := connectWS(t, ts, "client-owner")
	readObject(t, owner)
	observer := connectWS(t, ts, "client-observer")
	readObject(t, observer)
	sendObject(t, owner, map[string]any{"type": "takeover", "id": "take-1", "paused": true})
	control := readObject(t, owner)
	lease := readObject(t, owner)
	result := readObject(t, owner)
	if control["type"] != "control" || lease["type"] != "lease" || len(lease["token"].(string)) != 64 || result["id"] != "take-1" || result["ok"] != true {
		t.Fatal(control, lease, result)
	}
	observed := readObject(t, observer)
	if observed["type"] != "control" || observed["token"] != nil {
		t.Fatal("lease leaked", observed)
	}
	res := request(t, ts, "POST", "/api/act?token=test-secret", `{"action":"press_key","key":"Return"}`, "", "")
	if res.StatusCode != 409 {
		t.Errorf("paused action status %d", res.StatusCode)
	}
	closeResponse(res)
	e.mu.Lock()
	count := e.actions
	e.mu.Unlock()
	if count != 0 {
		t.Fatal("native action ran during takeover")
	}
	sendObject(t, observer, map[string]any{"type": "takeover", "id": "steal", "paused": false})
	rejection := readObject(t, observer)
	if rejection["ok"] != false {
		t.Fatal("observer released another owner's lease")
	}
	sendObject(t, owner, map[string]any{"type": "clipboard", "id": "clip", "text": "contraseña 日本語"})
	clip := readObject(t, owner)
	ack := readObject(t, owner)
	if clip["text"] != "contraseña 日本語" || ack["ok"] != true {
		t.Fatal(clip, ack)
	}
	res = request(t, ts, "GET", "/api/clipboard?token=test-secret&client=client-owner", "", "", "")
	if res.StatusCode != 403 {
		t.Errorf("clipboard bypass without lease: %d", res.StatusCode)
	}
	closeResponse(res)
	_ = owner.Close()
	released := readObject(t, observer)
	ctrl := released["control"].(map[string]any)
	if ctrl["paused"] != false {
		t.Fatal(released)
	}
	s.mu.Lock()
	token := s.manualToken
	s.mu.Unlock()
	if token != "" {
		t.Error("disconnected owner's lease retained")
	}
}

func TestActionsRejectStaleDisabledAndOutOfRange(t *testing.T) {
	s, e, _ := testServer(t)
	e.nodes = []model.Node{{ID: "disabled", States: []string{"disabled"}}, {ID: "button", Bounds: model.Rect{X: 10, Y: 10, Width: 20, Height: 20}}}
	px, py := 900, 10
	for _, a := range []model.Action{{Node: "gone", Action: "click"}, {Node: "disabled", Action: "click"}, {Action: "click", X: &px, Y: &py}} {
		if err := s.act(context.Background(), a); err == nil {
			t.Errorf("accepted invalid action %#v", a)
		}
	}
	e.mu.Lock()
	count := e.actions
	e.mu.Unlock()
	if count != 0 {
		t.Error("invalid action reached native layer")
	}
	if err := s.act(context.Background(), model.Action{Node: "button", Action: "click"}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	count = e.actions
	e.mu.Unlock()
	if count != 1 {
		t.Error("valid action not executed")
	}
}

func TestRejectNonLoopbackConfiguration(t *testing.T) {
	_, err := New(Config{Address: "0.0.0.0:5000", RFBSocket: filepath.Join(t.TempDir(), "rfb.sock")}, &fakeEngine{}, &fakeDesktop{}, nil, nil)
	if err == nil {
		t.Fatal("accepted external binding")
	}
}

func TestRejectTCPRFBBackend(t *testing.T) {
	for _, address := range []string{"", "127.0.0.1:5900", "rfb.sock", "@rfb"} {
		_, err := New(Config{Address: "127.0.0.1:0", RFBSocket: address}, &fakeEngine{}, &fakeDesktop{}, nil, nil)
		if err == nil {
			t.Errorf("accepted RFB backend %q without an absolute socket path", address)
		}
	}
}

func TestSlowSemanticClientIsDisconnected(t *testing.T) {
	s, _, ts := testServer(t)
	ws := connectWS(t, ts, "client-slow")
	readObject(t, ws)
	// A separate unconsumed queue exercises the overflow policy deterministically.
	c := &client{conn: ws, queue: make(chan []byte, 1), done: make(chan struct{})}
	b, _ := json.Marshal(map[string]string{"type": "test"})
	s.mu.Lock()
	s.enqueueLocked(c, b)
	s.enqueueLocked(c, b)
	s.mu.Unlock()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("full client queue did not disconnect")
	}
}
