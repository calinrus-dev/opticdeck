// Package desktop talks only to the explicitly selected virtual X11 server.
package desktop

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/calinrus-dev/semadesk/internal/model"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

const maxClipboard = 1 << 20

type Window struct {
	ID     uint32
	PID    uint32
	Name   string
	Bounds model.Rect
	Frame  model.Rect
	Z      int
}

type Desktop struct {
	conn    *xgb.Conn
	root    xproto.Window
	owner   xproto.Window
	atoms   map[string]xproto.Atom
	events  chan struct{}
	done    chan struct{}
	once    sync.Once
	inputMu sync.Mutex
	clipMu  sync.Mutex
	clip    string
	pending map[xproto.Window]chan xproto.SelectionNotifyEvent
}

// Open never consults DISPLAY: callers must provide a local virtual display.
func Open(display string) (*Desktop, error) {
	if !strings.HasPrefix(display, ":") || strings.ContainsAny(display, "/\\ \t\r\n") {
		return nil, errors.New("an explicit local X11 display such as :100 is required")
	}
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("connect X11 %s: %w", display, err)
	}
	d := &Desktop{conn: c, root: xproto.Setup(c).DefaultScreen(c).Root, atoms: make(map[string]xproto.Atom), events: make(chan struct{}, 1), done: make(chan struct{}), pending: make(map[xproto.Window]chan xproto.SelectionNotifyEvent)}
	fail := func(err error) (*Desktop, error) { c.Close(); return nil, err }
	if err = xtest.Init(c); err != nil {
		return fail(fmt.Errorf("XTEST is required: %w", err))
	}
	for _, name := range []string{"_NET_CLIENT_LIST_STACKING", "_NET_FRAME_EXTENTS", "_NET_WM_PID", "_NET_WM_NAME", "_NET_ACTIVE_WINDOW", "WM_NAME", "UTF8_STRING", "CLIPBOARD", "TARGETS", "TEXT", "STRING", "INCR", "SEMADESK_CLIPBOARD"} {
		a, e := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
		if e != nil {
			return fail(e)
		}
		d.atoms[name] = a.Atom
	}
	d.owner, err = xproto.NewWindowId(c)
	if err != nil {
		return fail(err)
	}
	if err = xproto.CreateWindowChecked(c, 0, d.owner, d.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return fail(err)
	}
	if err = xproto.ChangeWindowAttributesChecked(c, d.root, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange | xproto.EventMaskSubstructureNotify | xproto.EventMaskStructureNotify}).Check(); err != nil {
		return fail(err)
	}
	go d.eventLoop()
	return d, nil
}

func (d *Desktop) Close() error {
	d.once.Do(func() { d.conn.Close(); <-d.done })
	return nil
}

func (d *Desktop) Events() <-chan struct{} { return d.events }

func (d *Desktop) changed() {
	select {
	case d.events <- struct{}{}:
	default:
	}
}

func (d *Desktop) eventLoop() {
	defer close(d.done)
	for {
		event, err := d.conn.WaitForEvent()
		if event == nil {
			if err != nil {
				d.changed()
				continue // An asynchronous BadWindow is not a lost connection.
			}
			return
		}
		switch e := event.(type) {
		case xproto.SelectionRequestEvent:
			d.selectionRequest(e)
		case xproto.SelectionNotifyEvent:
			d.clipMu.Lock()
			ch := d.pending[e.Requestor]
			d.clipMu.Unlock()
			if ch != nil {
				select {
				case ch <- e:
				default:
				}
			}
		case xproto.ConfigureNotifyEvent, xproto.PropertyNotifyEvent, xproto.MapNotifyEvent, xproto.UnmapNotifyEvent, xproto.DestroyNotifyEvent, xproto.ReparentNotifyEvent, xproto.FocusInEvent, xproto.FocusOutEvent:
			d.changed()
		}
	}
}

func (d *Desktop) Size() (int, int) {
	g, err := xproto.GetGeometry(d.conn, xproto.Drawable(d.root)).Reply()
	if err != nil || g == nil {
		return 0, 0
	}
	return int(g.Width), int(g.Height)
}

func (d *Desktop) property(w xproto.Window, name string, limit uint32) (*xproto.GetPropertyReply, error) {
	return xproto.GetProperty(d.conn, false, w, d.atoms[name], xproto.GetPropertyTypeAny, 0, limit).Reply()
}

func uints(p *xproto.GetPropertyReply) []uint32 {
	if p == nil || p.Format != 32 {
		return nil
	}
	r := make([]uint32, len(p.Value)/4)
	for i := range r {
		r[i] = xgb.Get32(p.Value[i*4:])
	}
	return r
}

func (d *Desktop) geometry(w xproto.Window) (model.Rect, error) {
	g, err := xproto.GetGeometry(d.conn, xproto.Drawable(w)).Reply()
	if err != nil {
		return model.Rect{}, err
	}
	p, err := xproto.TranslateCoordinates(d.conn, w, d.root, 0, 0).Reply()
	if err != nil {
		return model.Rect{}, err
	}
	return model.Rect{X: int(p.DstX), Y: int(p.DstY), Width: int(g.Width), Height: int(g.Height)}, nil
}

// Windows includes managed clients and mapped override-redirect popups. Root
// sibling ranks reconcile popup order with the EWMH client stacking list.
func (d *Desktop) Windows() ([]Window, error) {
	rootTree, err := xproto.QueryTree(d.conn, d.root).Reply()
	if err != nil {
		return nil, err
	}
	stack, err := d.property(d.root, "_NET_CLIENT_LIST_STACKING", 16384)
	if err != nil {
		return nil, err
	}
	clients := uints(stack)
	ranks := make(map[xproto.Window]int, len(rootTree.Children))
	for i, w := range rootTree.Children {
		ranks[w] = i
	}
	frames := make(map[xproto.Window]bool)
	type ranked struct {
		Window
		rank, order int
	}
	var result []ranked
	add := func(w xproto.Window, rank, order int) {
		if w == d.owner {
			return
		}
		a, e := xproto.GetWindowAttributes(d.conn, w).Reply()
		if e != nil || a.MapState != xproto.MapStateViewable || a.Class != xproto.WindowClassInputOutput {
			return
		}
		b, e := d.geometry(w)
		if e != nil || b.Width <= 0 || b.Height <= 0 {
			return
		}
		item := Window{ID: uint32(w), Bounds: b, Frame: b}
		if p, e := d.property(w, "_NET_WM_PID", 1); e == nil {
			if v := uints(p); len(v) > 0 {
				item.PID = v[0]
			}
		}
		if p, e := d.property(w, "_NET_WM_NAME", 1024); e == nil {
			item.Name = strings.ToValidUTF8(string(p.Value), "�")
		}
		if item.Name == "" {
			if p, e := d.property(w, "WM_NAME", 1024); e == nil {
				item.Name = strings.ToValidUTF8(string(p.Value), "�")
			}
		}
		if p, e := d.property(w, "_NET_FRAME_EXTENTS", 4); e == nil {
			if v := uints(p); len(v) == 4 && v[0] < 65536 && v[1] < 65536 && v[2] < 65536 && v[3] < 65536 {
				item.Frame = model.Rect{X: b.X - int(v[0]), Y: b.Y - int(v[2]), Width: b.Width + int(v[0]+v[1]), Height: b.Height + int(v[2]+v[3])}
			}
		}
		_ = xproto.ChangeWindowAttributesChecked(d.conn, w, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange | xproto.EventMaskStructureNotify | xproto.EventMaskFocusChange}).Check()
		result = append(result, ranked{Window: item, rank: rank, order: order})
	}
	for i, id := range clients {
		w := xproto.Window(id)
		ancestor := w
		for depth := 0; depth < 32; depth++ {
			tree, e := xproto.QueryTree(d.conn, ancestor).Reply()
			if e != nil || tree.Parent == 0 || tree.Parent == d.root {
				break
			}
			ancestor = tree.Parent
		}
		frames[ancestor] = true
		rank, ok := ranks[ancestor]
		if !ok {
			rank = i
		}
		add(w, rank, i)
	}
	for i, w := range rootTree.Children {
		if !frames[w] {
			add(w, i, len(clients)+i)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].rank == result[j].rank {
			return result[i].order < result[j].order
		}
		return result[i].rank < result[j].rank
	})
	out := make([]Window, len(result))
	for i := range result {
		out[i] = result[i].Window
		out[i].Z = i
	}
	return out, nil
}

func (d *Desktop) fake(kind, detail byte, x, y int) error {
	return xtest.FakeInputChecked(d.conn, kind, detail, 0, d.root, int16(x), int16(y), 0).Check()
}

func (d *Desktop) Click(x, y, button, count int) error {
	d.inputMu.Lock()
	defer d.inputMu.Unlock()
	w, h := d.Size()
	if x < 0 || y < 0 || x >= w || y >= h || x > 32767 || y > 32767 {
		return errors.New("click outside virtual framebuffer")
	}
	if (button != 1 && button != 2 && button != 3) || (count != 1 && count != 2) {
		return errors.New("unsupported mouse button or count")
	}
	if err := d.fake(xproto.MotionNotify, 0, x, y); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		if err := d.fake(xproto.ButtonPress, byte(button), 0, 0); err != nil {
			_ = d.fake(xproto.ButtonRelease, byte(button), 0, 0)
			return err
		}
		if err := d.fake(xproto.ButtonRelease, byte(button), 0, 0); err != nil {
			return err
		}
		if count == 2 && i == 0 {
			time.Sleep(70 * time.Millisecond)
		}
	}
	return nil
}

func (d *Desktop) Focus(window uint32) error {
	d.inputMu.Lock()
	defer d.inputMu.Unlock()
	w := xproto.Window(window)
	a, err := xproto.GetWindowAttributes(d.conn, w).Reply()
	if err != nil {
		return err
	}
	if a.MapState != xproto.MapStateViewable {
		return errors.New("window is not viewable")
	}
	event := xproto.ClientMessageEvent{Format: 32, Window: w, Type: d.atoms["_NET_ACTIVE_WINDOW"], Data: xproto.ClientMessageDataUnionData32New([]uint32{2, 0, 0, 0, 0})}
	if err = xproto.SendEventChecked(d.conn, false, d.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes())).Check(); err != nil {
		return err
	}
	return xproto.SetInputFocusChecked(d.conn, xproto.InputFocusPointerRoot, w, xproto.TimeCurrentTime).Check()
}

// SetClipboard owns CLIPBOARD and responds to UTF-8 ICCCM selection requests.
func (d *Desktop) SetClipboard(text string) error {
	if !utf8.ValidString(text) || len(text) > maxClipboard {
		return errors.New("clipboard must be valid UTF-8 and at most 1 MiB")
	}
	d.clipMu.Lock()
	d.clip = text
	d.clipMu.Unlock()
	return xproto.SetSelectionOwnerChecked(d.conn, d.owner, d.atoms["CLIPBOARD"], xproto.TimeCurrentTime).Check()
}

func (d *Desktop) selectionRequest(e xproto.SelectionRequestEvent) {
	property := e.Property
	if property == 0 {
		property = e.Target
	}
	d.clipMu.Lock()
	text := d.clip
	d.clipMu.Unlock()
	var data []byte
	format := byte(8)
	kind := d.atoms["UTF8_STRING"]
	ok := e.Selection == d.atoms["CLIPBOARD"]
	switch e.Target {
	case d.atoms["TARGETS"]:
		kind, format = xproto.AtomAtom, 32
		data = make([]byte, 16)
		for i, a := range []xproto.Atom{d.atoms["TARGETS"], d.atoms["UTF8_STRING"], d.atoms["TEXT"], d.atoms["STRING"]} {
			xgb.Put32(data[i*4:], uint32(a))
		}
	case d.atoms["UTF8_STRING"], d.atoms["TEXT"]:
		data = []byte(text)
	case d.atoms["STRING"]:
		kind = d.atoms["STRING"]
		for _, r := range text {
			if r > 255 {
				data = append(data, '?')
			} else {
				data = append(data, byte(r))
			}
		}
	default:
		ok = false
	}
	if ok {
		// Chunks stay below even the minimum core X11 request-size limit.
		if len(data) == 0 {
			ok = xproto.ChangePropertyChecked(d.conn, xproto.PropModeReplace, e.Requestor, property, kind, format, 0, nil).Check() == nil
		}
		for offset := 0; offset < len(data) && ok; {
			n := len(data) - offset
			if n > 60000 {
				n = 60000
			}
			mode := byte(xproto.PropModeAppend)
			if offset == 0 {
				mode = xproto.PropModeReplace
			}
			ok = xproto.ChangePropertyChecked(d.conn, mode, e.Requestor, property, kind, format, uint32(n)/uint32(format/8), data[offset:offset+n]).Check() == nil
			offset += n
		}
	}
	if !ok {
		property = 0
	}
	notification := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: property}
	_ = xproto.SendEventChecked(d.conn, false, e.Requestor, 0, string(notification.Bytes())).Check()
}

// Clipboard requests a separate hidden requestor window per call, so late
// SelectionNotify events after a timeout cannot satisfy a later request.
func (d *Desktop) Clipboard() (string, error) {
	owner, err := xproto.GetSelectionOwner(d.conn, d.atoms["CLIPBOARD"]).Reply()
	if err != nil {
		return "", err
	}
	if owner.Owner == 0 {
		return "", nil
	}
	if owner.Owner == d.owner {
		d.clipMu.Lock()
		defer d.clipMu.Unlock()
		return d.clip, nil
	}
	w, err := xproto.NewWindowId(d.conn)
	if err != nil {
		return "", err
	}
	if err = xproto.CreateWindowChecked(d.conn, 0, w, d.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
		return "", err
	}
	defer xproto.DestroyWindow(d.conn, w)
	ch := make(chan xproto.SelectionNotifyEvent, 2)
	d.clipMu.Lock()
	d.pending[w] = ch
	d.clipMu.Unlock()
	defer func() { d.clipMu.Lock(); delete(d.pending, w); d.clipMu.Unlock() }()
	for _, target := range []xproto.Atom{d.atoms["UTF8_STRING"], d.atoms["STRING"]} {
		if err = xproto.ConvertSelectionChecked(d.conn, w, d.atoms["CLIPBOARD"], target, d.atoms["SEMADESK_CLIPBOARD"], xproto.TimeCurrentTime).Check(); err != nil {
			return "", err
		}
		var e xproto.SelectionNotifyEvent
		select {
		case e = <-ch:
		case <-time.After(2 * time.Second):
			return "", errors.New("clipboard owner did not respond")
		case <-d.done:
			return "", errors.New("X11 connection closed")
		}
		if e.Property == 0 {
			continue
		}
		p, err := xproto.GetProperty(d.conn, true, w, e.Property, xproto.GetPropertyTypeAny, 0, maxClipboard/4+1).Reply()
		if err != nil {
			return "", err
		}
		if p.Type == d.atoms["INCR"] {
			return "", errors.New("incremental INCR clipboard transfer is unsupported")
		}
		if p.BytesAfter > 0 || len(p.Value) > maxClipboard {
			return "", errors.New("clipboard exceeds 1 MiB")
		}
		if p.Format != 8 {
			return "", errors.New("clipboard does not contain 8-bit text")
		}
		if p.Type == d.atoms["STRING"] {
			r := make([]rune, len(p.Value))
			for i, b := range p.Value {
				r[i] = rune(b)
			}
			return string(r), nil
		}
		if !utf8.Valid(p.Value) {
			return "", errors.New("clipboard text is not UTF-8")
		}
		return string(p.Value), nil
	}
	return "", errors.New("clipboard owner supports neither UTF8_STRING nor STRING")
}

func (d *Desktop) Paste(text string) error {
	if err := d.SetClipboard(text); err != nil {
		return err
	}
	return d.Shortcut("Ctrl+v")
}
