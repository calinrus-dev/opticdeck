// Package server exposes an authenticated local semantic desktop and its RFB view.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/calinrus-dev/semadesk/internal/web"
	"github.com/gorilla/websocket"
)

const maxMessage = 1 << 20

// Engine and Desktop keep HTTP policy testable without an X server.
type Engine interface {
	Snapshot(context.Context) ([]model.Node, error)
	Changes() <-chan struct{}
	Act(context.Context, model.Action, model.Node) error
}
type Desktop interface {
	Size() (int, int)
	Events() <-chan struct{}
	SetClipboard(string) error
	Clipboard() (string, error)
}
type Config struct {
	Address       string
	Token         string
	RFBSocket     string
	Width, Height int
}
type client struct {
	id    string
	conn  *websocket.Conn
	queue chan []byte
	done  chan struct{}
	once  sync.Once
}

func (c *client) close() { c.once.Do(func() { close(c.done); _ = c.conn.Close() }) }

// Server owns all control arbitration. gate serializes both semantic actions
// and manual RFB input with takeover transitions; mu protects snapshots/clients.
type Server struct {
	refreshMu        sync.Mutex
	refreshes        chan struct{}
	manualToken      string
	cfg              Config
	engine           Engine
	desktop          Desktop
	launch           func([]string) (int, error)
	stop             func()
	http             *http.Server
	ctx              context.Context
	cancel           context.CancelFunc
	gate             sync.Mutex
	mu               sync.Mutex
	clients          map[string]*client
	rfbs             map[*websocket.Conn]net.Conn
	control          model.Control
	nodes            map[string]model.Node
	snapshot         model.Snapshot
	host             string
	lastError        string
	agentStatus      *model.AgentStatus
	actionCancel     context.CancelFunc
	capturing        atomic.Bool
	hostView         bool
	hostInputToken   string
	hostMonitorScale float64
	hostController   *websocket.Conn
}

type diff struct {
	Type     string       `json:"type"`
	Base     uint64       `json:"base"`
	Revision uint64       `json:"revision"`
	Upsert   []model.Node `json:"upsert"`
	Remove   []string     `json:"remove"`
	Width    int          `json:"width"`
	Height   int          `json:"height"`
}
type message struct {
	Type string `json:"type"`
	model.Action
	Paused bool `json:"paused"`
}

func New(cfg Config, engine Engine, desktop Desktop, launch func([]string) (int, error), stop func()) (*Server, error) {
	if engine == nil || desktop == nil {
		return nil, errors.New("desktop and semantic engine are required")
	}
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil || host != "127.0.0.1" {
		return nil, errors.New("HTTP address must be 127.0.0.1:port")
	}
	if !filepath.IsAbs(cfg.RFBSocket) {
		return nil, errors.New("RFB backend must be an absolute Unix socket path")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{cfg: cfg, engine: engine, desktop: desktop, launch: launch, stop: stop, ctx: ctx, cancel: cancel, refreshes: make(chan struct{}, 1), clients: map[string]*client{}, rfbs: map[*websocket.Conn]net.Conn{}, nodes: map[string]model.Node{}}
	w, h := desktop.Size()
	if w <= 0 || h <= 0 {
		w, h = cfg.Width, cfg.Height
	}
	if w <= 0 || h <= 0 {
		cancel()
		return nil, errors.New("invalid framebuffer size")
	}
	s.snapshot = model.Snapshot{Type: "snapshot", Revision: 1, Width: w, Height: h, Nodes: []model.Node{}}
	s.http = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	return s, nil
}

func (s *Server) Serve(listener net.Listener) error {
	tcp, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.Equal(net.ParseIP("127.0.0.1")) {
		_ = listener.Close()
		return errors.New("HTTP listener must bind IPv4 loopback")
	}
	s.mu.Lock()
	s.host = listener.Addr().String()
	s.mu.Unlock()
	if !s.hostView {
		s.refresh()
		go s.watch()
	}
	err := s.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Close(ctx context.Context) error {
	s.cancel()
	s.gate.Lock()
	s.mu.Lock()
	for _, c := range s.clients {
		c.close()
	}
	s.closeRFBLocked()
	s.mu.Unlock()
	s.gate.Unlock()
	return s.http.Shutdown(ctx)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.status)
	mux.HandleFunc("/api/tree", s.tree)
	mux.HandleFunc("/api/act", s.actionHTTP)
	mux.HandleFunc("/api/launch", s.launchHTTP)
	mux.HandleFunc("/api/stop", s.stopHTTP)
	mux.HandleFunc("/api/control", s.pauseHTTP)
	mux.HandleFunc("/api/agent-status", s.agentStatusHTTP)
	mux.HandleFunc("/api/capture", s.captureHTTP)
	mux.HandleFunc("/api/clipboard", s.clipboardHTTP)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/rfb", s.handleRFB)
	mux.Handle("/", http.FileServer(http.FS(web.FS())))
	return s.authenticate(mux)
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	cookieName := "semadesk_session"
	if s.hostView {
		cookieName = "opticdeck_host_session"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		s.mu.Lock()
		host := s.host
		s.mu.Unlock()
		if host == "" {
			host = s.cfg.Address
		}
		if r.Host != host {
			writeError(w, http.StatusForbidden, "invalid Host")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+host {
			writeError(w, http.StatusForbidden, "foreign Origin")
			return
		}
		if r.Method == http.MethodOptions {
			writeError(w, http.StatusMethodNotAllowed, "unsupported method")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("Authorization") == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			if c, e := r.Cookie(cookieName); e == nil {
				token = c.Value
			}
		}
		if s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "session token required")
			return
		}
		if s.cfg.Token != "" && r.URL.Query().Get("token") != "" && r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.cfg.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, err string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": err})
}
func method(w http.ResponseWriter, r *http.Request, m string) bool {
	if r.Method != m {
		w.Header().Set("Allow", m)
		writeError(w, http.StatusMethodNotAllowed, "unsupported method")
		return false
	}
	return true
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessage)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "expected one JSON request")
		return false
	}
	return true
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	s.mu.Lock()
	result := map[string]any{"ok": true, "revision": s.snapshot.Revision, "width": s.snapshot.Width, "height": s.snapshot.Height, "nodes": len(s.nodes), "control": s.control, "semantic_error": s.lastError, "agent_status": s.agentStatus}
	s.mu.Unlock()
	writeJSON(w, 200, result)
}
func (s *Server) snapshotLocked() model.Snapshot {
	result := s.snapshot
	result.Control = s.control
	result.AgentStatus = s.agentStatus
	return result
}
func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	fresh := r.URL.Query().Get("fresh") == "1"
	if fresh {
		s.refresh()
	}
	s.mu.Lock()
	result := s.snapshotLocked()
	failed := s.lastError != ""
	s.mu.Unlock()
	if fresh && failed {
		writeError(w, http.StatusServiceUnavailable, "cannot read the current desktop")
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) watch() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	changes, events := s.engine.Changes(), s.desktop.Events()
	var timer *time.Timer
	var timerC <-chan time.Time
	schedule := func() {
		if timer == nil {
			timer = time.NewTimer(60 * time.Millisecond)
			timerC = timer.C
		}
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-s.ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				changes = nil
			} else {
				schedule()
			}
		case _, ok := <-events:
			if !ok {
				events = nil
			} else {
				schedule()
			}
		case <-s.refreshes:
			s.refresh()
		case <-ticker.C:
			s.refresh()
		case <-timerC:
			timer = nil
			timerC = nil
			s.refresh()
		}
	}
}
func (s *Server) refresh() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	nodes, err := s.engine.Snapshot(ctx)
	if err != nil {
		s.mu.Lock()
		s.lastError = "semantic reconciliation failed"
		s.mu.Unlock()
		return
	}
	s.publish(nodes)
}
func (s *Server) publish(nodes []model.Node) {
	w, h := s.desktop.Size()
	if nodes == nil {
		nodes = []model.Node{}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]model.Node, len(nodes))
	for _, n := range nodes {
		next[n.ID] = n
	}
	d := diff{Type: "diff", Base: s.snapshot.Revision, Upsert: []model.Node{}, Remove: []string{}, Width: w, Height: h}
	for _, n := range nodes {
		if old, ok := s.nodes[n.ID]; !ok || !reflect.DeepEqual(old, n) {
			d.Upsert = append(d.Upsert, n)
		}
	}
	for id := range s.nodes {
		if _, ok := next[id]; !ok {
			d.Remove = append(d.Remove, id)
		}
	}
	sort.Strings(d.Remove)
	if len(d.Upsert) == 0 && len(d.Remove) == 0 && w == s.snapshot.Width && h == s.snapshot.Height {
		s.lastError = ""
		return
	}
	if w <= 0 || h <= 0 {
		w, h = s.snapshot.Width, s.snapshot.Height
		d.Width, d.Height = w, h
	}
	d.Revision = s.snapshot.Revision + 1
	s.nodes = next
	s.snapshot = model.Snapshot{Type: "snapshot", Revision: d.Revision, Width: w, Height: h, Nodes: nodes, Control: s.control}
	s.lastError = ""
	s.broadcastLocked(d)
}
func (s *Server) broadcastLocked(v any) {
	b, e := json.Marshal(v)
	if e != nil {
		return
	}
	for _, c := range s.clients {
		s.enqueueLocked(c, b)
	}
}
func (s *Server) enqueueLocked(c *client, b []byte) {
	select {
	case c.queue <- b:
	case <-c.done:
	default:
		c.close()
	}
}
func (s *Server) send(c *client, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		return
	}
	s.mu.Lock()
	s.enqueueLocked(c, b)
	s.mu.Unlock()
}

var errStaleObservation = errors.New("the observed control or desktop revision changed; read the current desktop and choose the target again")

func (s *Server) actionHTTP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var a model.Action
	if !readJSON(w, r, &a) {
		return
	}
	err := s.act(r.Context(), a)
	if errors.Is(err, errStaleObservation) {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": "stale_observation", "error": err.Error()})
		return
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": a.ID})
}
func (s *Server) act(ctx context.Context, a model.Action) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	actionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stopOnClose := context.AfterFunc(s.ctx, cancel)
	defer stopOnClose()
	s.mu.Lock()
	if s.control.Paused {
		s.mu.Unlock()
		return errors.New("agent actions are paused")
	}
	s.actionCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.actionCancel = nil
		s.mu.Unlock()
	}()
	if len(a.Text) > maxMessage || !utf8.ValidString(a.Text) || len(a.Key) > 256 || len(a.Node) > 4096 ||
		(a.ExpectName != nil && (a.Node == "" || len(*a.ExpectName) > 1024)) {
		return errors.New("invalid action payload")
	}
	switch a.Action {
	case "click", "double_click", "right_click", "set_text", "press_key", "hold_key", "shortcut", "focus":
	default:
		return errors.New("unsupported action")
	}
	if a.Action == "hold_key" && (a.Key == "" || a.DurationMS < 100 || a.DurationMS > 2000) {
		return errors.New("hold_key requires a key and duration_ms between 100 and 2000")
	}
	// A fresh scan makes an ID insufficient to act on a widget that was removed,
	// hidden, moved or covered since the last browser update.
	if a.Node != "" || a.X != nil || a.Y != nil || a.ExpectRevision != nil {
		s.refreshMu.Lock()
		nodes, err := s.engine.Snapshot(actionCtx)
		if err == nil {
			s.publish(nodes)
		}
		s.refreshMu.Unlock()
		if err != nil {
			return errors.New("cannot validate semantic target")
		}
	}
	s.mu.Lock()
	n, exists := s.nodes[a.Node]
	width, height := s.snapshot.Width, s.snapshot.Height
	revision := s.snapshot.Revision
	s.mu.Unlock()
	if (a.ExpectRevision != nil && *a.ExpectRevision != revision) || (a.ExpectName != nil && (!exists || *a.ExpectName != n.Name)) {
		return errStaleObservation
	}
	if a.Node != "" && !exists {
		return errors.New("stale or hidden semantic target")
	}
	if exists {
		for _, state := range n.States {
			if state == "disabled" || state == "defunct" {
				return errors.New("target is disabled or defunct")
			}
		}
	}
	if (a.X == nil) != (a.Y == nil) {
		return errors.New("both x and y are required")
	}
	if a.X != nil && (*a.X < 0 || *a.Y < 0 || *a.X >= width || *a.Y >= height) {
		return errors.New("coordinates outside framebuffer")
	}
	if a.X != nil && exists && (*a.X < n.Bounds.X || *a.Y < n.Bounds.Y || *a.X >= n.Bounds.X+n.Bounds.Width || *a.Y >= n.Bounds.Y+n.Bounds.Height) {
		return errors.New("coordinates outside semantic target")
	}
	if a.Node == "" && a.Action != "press_key" && a.Action != "hold_key" && a.Action != "shortcut" && a.X == nil {
		return errors.New("semantic target or coordinates required")
	}
	if err := actionCtx.Err(); err != nil {
		return err
	}
	err := s.engine.Act(actionCtx, a, n)
	safeErr := ""
	if err != nil {
		safeErr = "native action failed"
	}
	s.mu.Lock()
	s.broadcastLocked(map[string]any{"type": "log", "time": time.Now().UTC().Format(time.RFC3339Nano), "action": a.Action, "node": a.Node, "target_name": activityTargetName(n), "ok": err == nil, "error": safeErr})
	s.mu.Unlock()
	if err != nil {
		return errors.New(safeErr)
	}
	select {
	case s.refreshes <- struct{}{}:
	default:
	}
	return nil
}

func activityTargetName(n model.Node) string {
	switch n.Role {
	case "button", "checkbox", "radio", "menuitem", "menuitemcheckbox", "menuitemradio", "tab", "link", "slider":
	default:
		return ""
	}
	if n.VisionFallback || slices.Contains(n.States, "protected") || slices.Contains(n.States, "editable") ||
		(n.Value != "" && strings.Contains(n.Name, n.Value)) {
		return ""
	}
	name := []rune(strings.Join(strings.Fields(n.Name), " "))
	if len(name) > 96 {
		return string(name[:95]) + "…"
	}
	return string(name)
}

func (s *Server) launchHTTP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var v struct {
		Argv []string `json:"argv"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if len(v.Argv) == 0 || len(v.Argv) > 128 {
		writeError(w, 400, "argv must contain 1..128 arguments")
		return
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	paused := s.control.Paused
	s.mu.Unlock()
	if paused {
		writeError(w, 409, "agent actions are paused by human takeover")
		return
	}
	if s.launch == nil {
		writeError(w, 503, "application launch unavailable")
		return
	}
	pid, err := s.launch(v.Argv)
	if err != nil {
		writeError(w, 400, "application launch failed")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "pid": pid})
}
func (s *Server) stopHTTP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
	if s.stop != nil {
		go s.stop()
	}
}
func (s *Server) clipboardHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("client")
	if id == "" {
		id = r.Header.Get("X-SemaDesk-Client")
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeError(w, 405, "unsupported method")
		return
	}
	var text *string
	if r.Method == http.MethodPost {
		var v struct {
			Text string `json:"text"`
		}
		if !readJSON(w, r, &v) {
			return
		}
		text = &v.Text
	}
	lease := r.URL.Query().Get("lease")
	if lease == "" {
		writeError(w, 403, "human takeover lease required")
		return
	}
	result, err := s.clipboard(id, text, &lease)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "text": result})
}
func (s *Server) clipboard(id string, text *string, lease *string) (string, error) {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	allowed := s.control.Paused && s.control.Owner == id && s.clients[id] != nil
	if lease != nil {
		allowed = allowed && s.manualToken != "" && subtle.ConstantTimeCompare([]byte(*lease), []byte(s.manualToken)) == 1
	}
	s.mu.Unlock()
	if !allowed {
		return "", errors.New("clipboard access requires owning human takeover")
	}
	if text != nil {
		if len(*text) > maxMessage || !utf8.ValidString(*text) {
			return "", errors.New("invalid clipboard text")
		}
		if err := s.desktop.SetClipboard(*text); err != nil {
			return "", errors.New("native clipboard write failed")
		}
		return *text, nil
	}
	value, err := s.desktop.Clipboard()
	if err != nil {
		return "", errors.New("native clipboard read failed")
	}
	if len(value) > maxMessage {
		return "", errors.New("native clipboard exceeds size limit")
	}
	return value, nil
}
func (s *Server) upgrader() websocket.Upgrader {
	return websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		u, e := url.Parse(origin)
		return origin == "" || (e == nil && u.Scheme == "http" && u.Host == r.Host && u.Path == "")
	}}
}
func validClientID(v string) bool {
	if len(v) < 8 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("client")
	if !validClientID(id) {
		writeError(w, 400, "client must be 8..128 URL-safe characters")
		return
	}
	up := s.upgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{id: id, conn: conn, queue: make(chan []byte, 64), done: make(chan struct{})}
	s.gate.Lock()
	s.mu.Lock()
	if s.clients[id] != nil {
		s.mu.Unlock()
		s.gate.Unlock()
		c.close()
		return
	}
	s.clients[id] = c
	b, _ := json.Marshal(s.snapshotLocked())
	s.enqueueLocked(c, b)
	s.mu.Unlock()
	s.gate.Unlock()
	defer func() {
		s.gate.Lock()
		s.mu.Lock()
		if s.clients[id] == c {
			delete(s.clients, id)
			if s.control.Owner == id {
				s.control = model.Control{}
				s.manualToken = ""
				s.closeRFBLocked()
				s.broadcastLocked(map[string]any{"type": "control", "control": s.control})
			}
		}
		s.mu.Unlock()
		s.gate.Unlock()
		c.close()
	}()
	go s.writeClient(c)
	conn.SetReadLimit(maxMessage)
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			s.send(c, map[string]any{"type": "result", "ok": false, "error": "invalid JSON request"})
			continue
		}
		var actionErr error
		switch m.Type {
		case "snapshot":
			s.mu.Lock()
			b, _ := json.Marshal(s.snapshotLocked())
			s.enqueueLocked(c, b)
			s.mu.Unlock()
			continue
		case "action":
			actionErr = s.act(s.ctx, m.Action)
		case "takeover":
			actionErr = s.takeover(id, m.Paused)
		case "clipboard", "clipboard_get":
			var v *string
			if m.Type == "clipboard" {
				v = &m.Text
			}
			var text string
			text, actionErr = s.clipboard(id, v, nil)
			if actionErr == nil {
				s.send(c, map[string]any{"type": "clipboard", "text": text})
			}
		default:
			actionErr = errors.New("unsupported message type")
		}
		result := map[string]any{"type": "result", "id": m.ID, "ok": actionErr == nil}
		if actionErr != nil {
			result["error"] = actionErr.Error()
			if errors.Is(actionErr, errStaleObservation) {
				result["code"] = "stale_observation"
			}
		}
		s.send(c, result)
	}
}
func (s *Server) writeClient(c *client) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer c.close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-c.done:
			return
		case b := <-c.queue:
			_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if c.conn.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
		case <-ticker.C:
			if c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}
func (s *Server) takeover(id string, paused bool) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[id] == nil {
		return errors.New("unknown human client")
	}
	if s.control.Paused && s.control.Owner != id {
		return errors.New("another human owns takeover")
	}
	if paused {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return errors.New("cannot create takeover lease")
		}
		s.manualToken = hex.EncodeToString(secret[:])
		s.control = model.Control{Paused: true, Owner: id}
	} else {
		s.manualToken = ""
		s.control = model.Control{}
	}
	s.closeRFBLocked()
	s.broadcastLocked(map[string]any{"type": "control", "control": s.control})
	if paused {
		b, _ := json.Marshal(map[string]any{"type": "lease", "token": s.manualToken})
		s.enqueueLocked(s.clients[id], b)
	}
	return nil
}
func (s *Server) closeRFBLocked() {
	for ws, backend := range s.rfbs {
		_ = backend.Close()
		_ = ws.Close()
		delete(s.rfbs, ws)
	}
}
func (s *Server) String() string { return fmt.Sprintf("SemaDesk HTTP %s", s.cfg.Address) }
