// Package semantic projects AT-SPI2 objects into bounded, stable overlay nodes.
package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/calinrus-dev/semadesk/internal/desktop"
	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/godbus/dbus/v5"
)

const (
	accessible      = "org.a11y.atspi.Accessible"
	component       = "org.a11y.atspi.Component"
	actionInterface = "org.a11y.atspi.Action"
	textInterface   = "org.a11y.atspi.Text"
	editableText    = "org.a11y.atspi.EditableText"
	properties      = "org.freedesktop.DBus.Properties"
	registry        = "org.a11y.atspi.Registry"
	rootPath        = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
)

type objectRef struct {
	Bus  string
	Path dbus.ObjectPath
}
type actionInfo struct {
	Name        string
	Description string
	KeyBinding  string
}
type objectInfo struct {
	Ref        objectRef
	Interfaces map[string]bool
	Actions    []actionInfo
	Role       string
}

type Engine struct {
	conn      *dbus.Conn
	address   string
	desktop   *desktop.Desktop
	changes   chan struct{}
	signals   chan *dbus.Signal
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.RWMutex
	refs      map[string]objectInfo
	scanMu    sync.Mutex
	closeOnce sync.Once
}

func New(ctx context.Context, busAddress string, d *desktop.Desktop) (*Engine, error) {
	if busAddress == "" {
		return nil, errors.New("an isolated session bus address is required")
	}
	session, err := dbus.Connect(busAddress)
	if err != nil {
		return nil, fmt.Errorf("connect isolated session bus: %w", err)
	}
	defer session.Close()
	bus := session.Object("org.a11y.Bus", "/org/a11y/bus")
	var address string
	initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
	defer initCancel()
	if err = bus.CallWithContext(initCtx, "org.a11y.Bus.GetAddress", 0).Store(&address); err != nil {
		return nil, fmt.Errorf("start AT-SPI2 bus (install at-spi2-core): %w", err)
	}
	if !strings.HasPrefix(address, "unix:") {
		return nil, errors.New("AT-SPI2 did not report a local Unix bus address")
	}
	const statusInterface = "org.a11y.Status"
	if err = bus.CallWithContext(initCtx, properties+".Set", 0, statusInterface, "IsEnabled", dbus.MakeVariant(true)).Err; err != nil {
		return nil, fmt.Errorf("enable AT-SPI2: %w", err)
	}
	var status map[string]dbus.Variant
	if err = bus.CallWithContext(initCtx, properties+".GetAll", 0, statusInterface).Store(&status); err != nil {
		return nil, fmt.Errorf("read AT-SPI2 status: %w", err)
	}
	// Keep older accessibility bridges enabled without requiring a property
	// that recent AT-SPI releases no longer advertise.
	if _, ok := status["ScreenReaderEnabled"]; ok {
		if err = bus.CallWithContext(initCtx, properties+".Set", 0, statusInterface, "ScreenReaderEnabled", dbus.MakeVariant(true)).Err; err != nil {
			return nil, fmt.Errorf("enable AT-SPI2 ScreenReaderEnabled: %w", err)
		}
	}
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("connect AT-SPI2 bus: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &Engine{conn: conn, address: address, desktop: d, changes: make(chan struct{}, 1), signals: make(chan *dbus.Signal, 128), cancel: cancel, refs: map[string]objectInfo{}}
	conn.Signal(e.signals)
	for _, name := range []string{"Object", "Window", "Focus", "Cache"} {
		if err = conn.AddMatchSignal(dbus.WithMatchInterface("org.a11y.atspi.Event." + name)); err != nil {
			e.Close()
			return nil, err
		}
	}
	if err = conn.AddMatchSignal(dbus.WithMatchInterface("org.a11y.atspi.Cache")); err != nil {
		e.Close()
		return nil, err
	}
	if err = conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged")); err != nil {
		e.Close()
		return nil, err
	}
	reg := conn.Object(registry, "/org/a11y/atspi/registry")
	for _, event := range []string{"object:", "window:", "focus:"} {
		err = reg.CallWithContext(initCtx, registry+".RegisterEvent", 0, event, []string{}, "").Err
		if err != nil {
			err = call(ctx, reg, registry+".RegisterEvent", event, []string{}).Err
		}
		if err != nil {
			err = call(ctx, reg, registry+".RegisterEvent", event).Err
		}
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("register AT-SPI2 events: %w", err)
		}
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-e.signals:
				if !ok {
					return
				}
				select {
				case e.changes <- struct{}{}:
				default:
				}
			}
		}
	}()
	return e, nil
}

// CallWithContext bounds hung or dying applications independently of a scan.
func call(ctx context.Context, obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return obj.CallWithContext(ctx, method, 0, args...)
}

func (e *Engine) Changes() <-chan struct{} { return e.changes }
func (e *Engine) Address() string          { return e.address }
func (e *Engine) Close() error {
	var err error
	e.closeOnce.Do(func() { e.cancel(); e.conn.RemoveSignal(e.signals); err = e.conn.Close(); e.wg.Wait() })
	return err
}

func nodeID(r objectRef) string {
	h := sha256.Sum256([]byte(r.Bus + "\x00" + string(r.Path)))
	return "a-" + base64.RawURLEncoding.EncodeToString(h[:18])
}
func clean(s string, n int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// AT-SPI keybindings contain mnemonic;menu-sequence;accelerator. Expose one
// directly usable chord rather than asking agents to execute the delimiter text.
func keyBinding(s string) string {
	parts := strings.Split(s, ";")
	mnemonic := false
	if len(parts) > 1 {
		s = ""
		if len(parts) > 2 {
			s = parts[2]
		}
		if s == "" {
			s = parts[0]
			mnemonic = true
		}
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, ":") {
		return ""
	}
	modifiers := strings.NewReplacer("<Control>", "Ctrl+", "<Ctrl>", "Ctrl+", "<Primary>", "Ctrl+", "<Alt>", "Alt+", "<Shift>", "Shift+", "<Super>", "Super+", "<Meta>", "Super+")
	s = modifiers.Replace(s)
	if strings.ContainsAny(s, "<>") {
		return ""
	}
	if mnemonic && !strings.Contains(s, "+") && utf8.RuneCountInString(s) == 1 {
		s = "Alt+" + s
	}
	return clean(s, 256)
}
func stateHas(states []uint32, bit uint) bool {
	return int(bit/32) < len(states) && states[bit/32]&(1<<(bit%32)) != 0
}
func stateNames(states []uint32) []string {
	out := []string{}
	for _, s := range []struct {
		bit  uint
		name string
	}{{3, "busy"}, {4, "checked"}, {7, "editable"}, {9, "expandable"}, {11, "focusable"}, {12, "focused"}, {13, "has-tooltip"}, {14, "horizontal"}, {16, "modal"}, {17, "multi-line"}, {18, "multiselectable"}, {19, "opaque"}, {20, "pressed"}, {22, "selectable"}, {25, "showing"}, {26, "single-line"}, {29, "vertical"}, {30, "visible"}, {31, "manages-descendants"}, {32, "indeterminate"}, {33, "required"}, {36, "invalid"}, {37, "supports-autocompletion"}, {38, "selectable-text"}, {39, "default"}, {40, "visited"}, {41, "checkable"}, {42, "has-popup"}, {43, "read-only"}} {
		if stateHas(states, s.bit) {
			out = append(out, s.name)
		}
	}
	if !stateHas(states, 8) || !stateHas(states, 24) {
		out = append(out, "disabled")
	}
	if stateHas(states, 23) {
		out = append(out, "selected")
	}
	if stateHas(states, 10) {
		out = append(out, "expanded")
	}
	return out
}

func normalizeRole(role string, interfaces map[string]bool) string {
	r := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(role)), "_", " ")
	switch r {
	case "button", "push button", "toggle button", "push button menu":
		return "button"
	case "check box":
		return "checkbox"
	case "radio button":
		return "radio"
	case "menu item", "tearoff menu item":
		return "menuitem"
	case "check menu item":
		return "menuitemcheckbox"
	case "radio menu item":
		return "menuitemradio"
	case "text", "entry", "password text", "editbar":
		if interfaces[editableText] || r != "text" {
			return "textbox"
		}
		return "text"
	case "frame", "window", "dialog", "alert", "file chooser", "color chooser", "font chooser":
		return "dialog"
	case "combo box":
		return "combobox"
	case "list box", "list":
		return "listbox"
	case "list item":
		return "option"
	case "page tab":
		return "tab"
	case "page tab list":
		return "tablist"
	case "tree item":
		return "treeitem"
	case "tree", "tree table":
		return "tree"
	case "table":
		return "table"
	case "table cell":
		return "cell"
	case "table row":
		return "row"
	case "image", "icon":
		return "img"
	case "progress bar", "level bar":
		return "progressbar"
	case "spin button":
		return "spinbutton"
	case "scroll bar":
		return "scrollbar"
	case "menu bar":
		return "menubar"
	case "popup menu":
		return "menu"
	case "tool bar":
		return "toolbar"
	case "tool tip":
		return "tooltip"
	case "status bar":
		return "status"
	case "label", "static":
		return "text"
	case "document web", "web document", "document frame", "document text":
		return "document"
	case "heading", "link", "slider", "separator", "menu", "paragraph", "document":
		return r
	default:
		return "group"
	}
}

type scanEntry struct {
	ref        objectRef
	parent     string
	parentRole string
	depth      int
	pid        uint32
	window     uint32
}

func (e *Engine) Snapshot(ctx context.Context) ([]model.Node, error) {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	windows, err := e.desktop.Windows()
	if err != nil {
		return nil, fmt.Errorf("X11 windows: %w", err)
	}
	width, height := e.desktop.Size()
	screen := model.Rect{Width: width, Height: height}
	var children []objectRef
	if err = call(ctx, e.conn.Object(registry, rootPath), accessible+".GetChildren").Store(&children); err != nil {
		return nil, fmt.Errorf("AT-SPI2 registry: %w", err)
	}
	if len(children) > 2500 {
		children = children[:2500]
	}
	queue := make([]scanEntry, 0, len(children))
	for _, r := range children {
		queue = append(queue, scanEntry{ref: r})
	}
	refs := map[string]objectInfo{}
	nodes := []model.Node{}
	visited := map[string]bool{}
	pidCache := map[string]uint32{}
	for len(queue) > 0 && len(visited) < 2500 {
		if ctx.Err() != nil {
			break
		}
		q := queue[0]
		queue = queue[1:]
		if q.depth > 48 || q.ref.Bus == "" || !q.ref.Path.IsValid() || q.ref.Path == "/org/a11y/atspi/null" {
			continue
		}
		id := nodeID(q.ref)
		if visited[id] {
			continue
		}
		visited[id] = true
		obj := e.conn.Object(q.ref.Bus, q.ref.Path)
		var states []uint32
		if call(ctx, obj, accessible+".GetState").Store(&states) != nil || stateHas(states, 6) {
			continue
		}
		if q.pid == 0 {
			pid, ok := pidCache[q.ref.Bus]
			if !ok {
				_ = call(ctx, e.conn.BusObject(), "org.freedesktop.DBus.GetConnectionUnixProcessID", q.ref.Bus).Store(&pid)
				pidCache[q.ref.Bus] = pid
			}
			q.pid = pid
		}
		var rawInterfaces []string
		_ = call(ctx, obj, accessible+".GetInterfaces").Store(&rawInterfaces)
		interfaces := map[string]bool{}
		for _, i := range rawInterfaces {
			interfaces[i] = true
		}
		var role string
		if call(ctx, obj, accessible+".GetRoleName").Store(&role) != nil {
			continue
		}
		visible := stateHas(states, 25) && stateHas(states, 30)
		if visible && interfaces[component] {
			var ext struct{ X, Y, Width, Height int32 }
			if call(ctx, obj, component+".GetExtents", uint32(0)).Store(&ext) == nil {
				bounds := intersect(model.Rect{X: int(ext.X), Y: int(ext.Y), Width: int(ext.Width), Height: int(ext.Height)}, screen)
				if area(bounds) > 0 {
					w, ok := windowFor(bounds, role, q.parentRole, q.pid, q.window, windows)
					if ok {
						q.window = w.ID
						bounds = visibleBounds(intersect(bounds, w.Bounds), w.ID, w.Z, windows)
						if area(bounds) > 0 {
							n := model.Node{ID: id, Parent: q.parent, Role: normalizeRole(role, interfaces), States: stateNames(states), Actions: []string{}, Bounds: bounds, Window: w.ID, Z: w.Z, Depth: q.depth}
							var name dbus.Variant
							if call(ctx, obj, properties+".Get", accessible, "Name").Store(&name) == nil {
								n.Name, _ = name.Value().(string)
								n.Name = clean(n.Name, 1024)
							}
							info := objectInfo{Ref: q.ref, Interfaces: interfaces, Role: role}
							if interfaces[actionInterface] {
								_ = call(ctx, obj, actionInterface+".GetActions").Store(&info.Actions)
								if len(info.Actions) > 0 {
									n.Actions = append(n.Actions, "click")
									n.Shortcut = keyBinding(info.Actions[primaryAction(info.Actions)].KeyBinding)
								}
							}
							if interfaces[editableText] || (n.Role == "textbox" && stateHas(states, 7) && !stateHas(states, 43)) {
								n.Actions = append(n.Actions, "set_text")
							}
							if stateHas(states, 11) {
								n.Actions = append(n.Actions, "focus")
							}
							if strings.EqualFold(role, "password text") {
								n.States = append(n.States, "protected")
							} else if interfaces[textInterface] && (n.Role == "textbox" || n.Role == "text" || role == "terminal") {
								_ = call(ctx, obj, textInterface+".GetText", int32(0), int32(4096)).Store(&n.Value)
								n.Value = clean(n.Value, 8192)
							}
							if interfaces["org.a11y.atspi.Value"] && !strings.EqualFold(role, "password text") {
								var v dbus.Variant
								if call(ctx, obj, properties+".Get", "org.a11y.atspi.Value", "CurrentValue").Store(&v) == nil {
									n.Value = clean(fmt.Sprint(v.Value()), 128)
								}
							}
							nodes = append(nodes, n)
							refs[id] = info
							q.parent = id
						}
					}
				}
			}
		}
		// Hidden applications and structural parents may still own visible menus;
		// traverse them rather than relying on ancestor visibility propagation.
		var kids []objectRef
		if call(ctx, obj, accessible+".GetChildren").Store(&kids) == nil {
			// Bound pending traversal as well as visited nodes. A broad tree
			// must not multiply a 2500-node budget into millions of queued refs.
			remaining := max(0, 2500-len(visited)-len(queue))
			if len(kids) > remaining {
				kids = kids[:remaining]
			}
			for _, r := range kids {
				queue = append(queue, scanEntry{ref: r, parent: q.parent, parentRole: role, depth: q.depth + 1, pid: q.pid, window: q.window})
			}
		}
	}
	// Preserve an explicit visual route for X11 windows and leaf drawing areas
	// which offer no actionable accessibility descendants.
	actionable := map[uint32]bool{}
	for _, n := range nodes {
		if n.Role == "dialog" || n.Role == "group" {
			continue
		}
		for _, a := range n.Actions {
			if a == "click" || a == "set_text" {
				actionable[n.Window] = true
			}
		}
	}
	for _, w := range windows {
		if actionable[w.ID] {
			continue
		}
		b := visibleBounds(intersect(w.Bounds, screen), w.ID, w.Z, windows)
		if area(b) == 0 {
			continue
		}
		nodes = append(nodes, model.Node{ID: fmt.Sprintf("x-%d", w.ID), Role: "group", Name: clean(w.Name, 1024), States: []string{}, Actions: []string{"click", "double_click", "right_click", "focus", "press_key", "shortcut", "set_text"}, Bounds: b, Window: w.ID, Z: w.Z, VisionFallback: true})
	}
	parents := map[string]bool{}
	for _, n := range nodes {
		if n.Parent != "" {
			parents[n.Parent] = true
		}
	}
	for i := range nodes {
		n := &nodes[i]
		info, ok := refs[n.ID]
		if ok && !parents[n.ID] && (info.Role == "drawing area" || info.Role == "canvas" || info.Role == "embedded" || info.Role == "terminal") {
			n.VisionFallback = true
			if len(n.Actions) == 0 {
				n.Actions = []string{"click", "double_click", "right_click", "press_key", "shortcut", "set_text"}
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Z != nodes[j].Z {
			return nodes[i].Z < nodes[j].Z
		}
		if nodes[i].Depth != nodes[j].Depth {
			return nodes[i].Depth < nodes[j].Depth
		}
		return nodes[i].ID < nodes[j].ID
	})
	e.mu.Lock()
	e.refs = refs
	e.mu.Unlock()
	return nodes, nil
}

func (e *Engine) Act(ctx context.Context, a model.Action, n model.Node) error {
	for _, s := range n.States {
		if s == "disabled" {
			return errors.New("target is disabled")
		}
	}
	e.mu.RLock()
	info, hasRef := e.refs[n.ID]
	e.mu.RUnlock()
	if n.ID != "" && !hasRef && !n.VisionFallback {
		return errors.New("semantic target expired; read the current tree before acting")
	}
	var obj dbus.BusObject
	if hasRef {
		obj = e.conn.Object(info.Ref.Bus, info.Ref.Path)
	}
	focus := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if obj != nil && info.Interfaces[component] {
			var ok bool
			if err := call(ctx, obj, component+".GrabFocus").Store(&ok); err == nil && ok {
				return nil
			}
		}
		if n.Window == 0 {
			return errors.New("a focusable target is required")
		}
		if err := e.desktop.Focus(n.Window); err != nil {
			return err
		}
		if n.Role == "textbox" && n.Bounds.Width > 0 && n.Bounds.Height > 0 {
			return e.desktop.Click(n.Bounds.X+n.Bounds.Width/2, n.Bounds.Y+n.Bounds.Height/2, 1, 1)
		}
		return nil
	}
	x, y := n.Bounds.X+n.Bounds.Width/2, n.Bounds.Y+n.Bounds.Height/2
	if a.X != nil || a.Y != nil {
		if a.X == nil || a.Y == nil {
			return errors.New("x and y must be provided together")
		}
		x, y = *a.X, *a.Y
	}
	width, height := e.desktop.Size()
	if (a.Action == "click" || a.Action == "double_click" || a.Action == "right_click") && (x < 0 || y < 0 || x >= width || y >= height) {
		return errors.New("coordinates outside virtual display")
	}
	switch a.Action {
	case "click":
		if obj != nil && len(info.Actions) > 0 && a.X == nil {
			var ok bool
			if err := call(ctx, obj, actionInterface+".DoAction", int32(primaryAction(info.Actions))).Store(&ok); err != nil {
				// A timeout is ambiguous: a late native action must never be
				// followed automatically by another XTest click.
				return fmt.Errorf("AT-SPI2 DoAction: %w", err)
			} else if ok {
				return nil
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return e.desktop.Click(x, y, 1, 1)
	case "double_click":
		return e.desktop.Click(x, y, 1, 2)
	case "right_click":
		return e.desktop.Click(x, y, 3, 1)
	case "set_text":
		if len(a.Text) > 1024*1024 || !utf8.ValidString(a.Text) {
			return errors.New("text must be valid UTF-8 and at most 1 MiB")
		}
		if obj != nil && info.Interfaces[editableText] {
			var ok bool
			if err := call(ctx, obj, editableText+".SetTextContents", a.Text).Store(&ok); err != nil {
				return fmt.Errorf("AT-SPI2 SetTextContents: %w", err)
			} else if ok {
				return nil
			}
		}
		if err := focus(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if obj == nil || !info.Interfaces[component] {
			if err := e.desktop.Click(x, y, 1, 1); err != nil {
				return err
			}
		}
		if err := e.desktop.Shortcut("Ctrl+a"); err != nil {
			return err
		}
		return e.desktop.Paste(a.Text)
	case "focus":
		return focus()
	case "press_key":
		if a.Key == "" {
			return errors.New("key is required")
		}
		if n.ID != "" {
			if err := focus(); err != nil {
				return err
			}
		}
		return e.desktop.PressKey(a.Key)
	case "hold_key":
		if n.ID != "" {
			if err := focus(); err != nil {
				return err
			}
		}
		return e.desktop.HoldKey(ctx, a.Key, time.Duration(a.DurationMS)*time.Millisecond)
	case "shortcut":
		if a.Key == "" {
			return errors.New("key chord is required")
		}
		if n.ID != "" {
			if err := focus(); err != nil {
				return err
			}
		}
		return e.desktop.Shortcut(a.Key)
	default:
		return fmt.Errorf("unsupported action %q", a.Action)
	}
}

// Toolkit providers order actions differently. Pick an activation action
// before auxiliary ones such as showing a menu or moving focus.
func primaryAction(actions []actionInfo) int {
	for _, name := range []string{"click", "press", "activate", "toggle", "check", "uncheck", "select", "open", "expand", "collapse"} {
		for i, action := range actions {
			if strings.EqualFold(strings.TrimSpace(action.Name), name) {
				return i
			}
		}
	}
	return 0
}
