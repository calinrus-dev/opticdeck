package desktop

import (
	"os"
	"strings"
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// The display must belong to a disposable test session, never the user's host.
func TestVirtualDisplayIntegration(t *testing.T) {
	display := os.Getenv("SEMADESK_TEST_DISPLAY")
	if display == "" {
		t.Skip("set SEMADESK_TEST_DISPLAY to a disposable virtual X11 server")
	}
	a, err := Open(display)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(display)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	w, h := a.Size()
	if w < 10 || h < 10 {
		t.Fatalf("invalid framebuffer %dx%d", w, h)
	}
	if _, err := a.Windows(); err != nil {
		t.Fatal(err)
	}
	// Override-redirect windows exercise popups omitted from the WM's EWMH list.
	var created []xproto.Window
	defer func() {
		for _, window := range created {
			xproto.DestroyWindow(a.conn, window)
		}
	}()
	for i, bounds := range []struct{ x, y, width, height int16 }{{10, 20, 120, 80}, {30, 35, 40, 30}} {
		window, err := xproto.NewWindowId(a.conn)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, window)
		if err := xproto.CreateWindowChecked(a.conn, 0, window, a.root, bounds.x, bounds.y, uint16(bounds.width), uint16(bounds.height), 0, xproto.WindowClassInputOutput, 0, xproto.CwOverrideRedirect, []uint32{1}).Check(); err != nil {
			t.Fatal(err)
		}
		name := []byte("SemaDesk geometry fixture")
		if err := xproto.ChangePropertyChecked(a.conn, xproto.PropModeReplace, window, a.atoms["_NET_WM_NAME"], a.atoms["UTF8_STRING"], 8, uint32(len(name)), name).Check(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			extents := make([]byte, 16)
			for j, v := range []uint32{3, 5, 7, 11} {
				xgb.Put32(extents[j*4:], v)
			}
			if err := xproto.ChangePropertyChecked(a.conn, xproto.PropModeReplace, window, a.atoms["_NET_FRAME_EXTENTS"], xproto.AtomCardinal, 32, 4, extents).Check(); err != nil {
				t.Fatal(err)
			}
		}
		if err := xproto.MapWindowChecked(a.conn, window).Check(); err != nil {
			t.Fatal(err)
		}
	}
	windows, err := a.Windows()
	if err != nil {
		t.Fatal(err)
	}
	var lower, upper *Window
	for i := range windows {
		if windows[i].ID == uint32(created[0]) {
			lower = &windows[i]
		}
		if windows[i].ID == uint32(created[1]) {
			upper = &windows[i]
		}
	}
	if lower == nil || upper == nil {
		t.Fatalf("missing mapped popup fixtures: %+v", windows)
	}
	if lower.Bounds.X != 10 || lower.Bounds.Y != 20 || lower.Bounds.Width != 120 || lower.Frame.X != 7 || lower.Frame.Y != 13 || lower.Frame.Width != 128 || lower.Frame.Height != 98 {
		t.Fatalf("client/frame coordinates: %+v", lower)
	}
	if upper.Z <= lower.Z {
		t.Fatalf("popup stacking order reversed: lower=%+v upper=%+v", lower, upper)
	}
	text := "SemaDesk ñ 界 🔑\n" + strings.Repeat("chunk", 14000)
	if err := a.SetClipboard(text); err != nil {
		t.Fatal(err)
	}
	got, err := b.Clipboard()
	if err != nil {
		t.Fatal(err)
	}
	if got != text {
		t.Fatalf("UTF-8 selection round trip: got %d bytes, want %d", len(got), len(text))
	}
	if err := b.SetClipboard(""); err != nil {
		t.Fatal(err)
	}
	got, err = a.Clipboard()
	if err != nil || got != "" {
		t.Fatalf("empty clipboard: %q, %v", got, err)
	}
	if err := a.Click(5, 7, 1, 1); err != nil {
		t.Fatal(err)
	}
	p, err := xproto.QueryPointer(a.conn, a.root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if p.RootX != 5 || p.RootY != 7 {
		t.Fatalf("XTEST pointer at %d,%d", p.RootX, p.RootY)
	}
	if err := a.Click(-1, 0, 1, 1); err == nil {
		t.Fatal("accepted offscreen click")
	}
	if err := a.PressKey("Escape"); err != nil {
		t.Fatal(err)
	}
	if err := a.Shortcut("Ctrl+Shift+F12"); err != nil {
		t.Fatal(err)
	}
	if err := a.Shortcut("Unsupported+q"); err == nil {
		t.Fatal("accepted invalid modifier")
	}
}
