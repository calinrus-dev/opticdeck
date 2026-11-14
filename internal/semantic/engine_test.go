package semantic

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/calinrus-dev/semadesk/internal/desktop"
	"github.com/calinrus-dev/semadesk/internal/model"
)

func TestOcclusionNeverReturnsForegroundPixels(t *testing.T) {
	background := model.Rect{Width: 200, Height: 100}
	foreground := model.Rect{X: 80, Y: 20, Width: 100, Height: 50}
	windows := []desktop.Window{{ID: 1, Z: 0, Bounds: background, Frame: background}, {ID: 2, Z: 1, Bounds: foreground, Frame: foreground}}
	r := visibleBounds(background, 1, 0, windows)
	if area(r) == 0 {
		t.Fatal("visible background was discarded")
	}
	if area(intersect(r, foreground)) != 0 {
		t.Fatalf("background hitbox overlaps foreground: %+v", r)
	}
	full := append(windows, desktop.Window{ID: 3, Z: 2, Bounds: background, Frame: background})
	if area(visibleBounds(background, 1, 0, full)) != 0 {
		t.Fatal("fully hidden window remained actionable")
	}
}

func TestSubtractConservesVisibleArea(t *testing.T) {
	a := model.Rect{X: 10, Y: 10, Width: 100, Height: 80}
	for _, b := range []model.Rect{{X: 30, Y: 20, Width: 20, Height: 20}, {Width: 1000, Height: 1000}, {X: 500, Y: 500, Width: 1, Height: 1}, {X: 5, Y: 5, Width: 25, Height: 25}} {
		var total int64
		parts := subtract(a, b)
		for i, p := range parts {
			total += area(p)
			if area(intersect(p, b)) != 0 {
				t.Fatalf("part overlaps cover: %+v", p)
			}
			for j := i + 1; j < len(parts); j++ {
				if area(intersect(p, parts[j])) != 0 {
					t.Fatal("parts overlap")
				}
			}
		}
		if total != area(a)-area(intersect(a, b)) {
			t.Fatalf("area changed: %d", total)
		}
	}
}

func TestSameProcessWindowsUseGeometry(t *testing.T) {
	windows := []desktop.Window{{ID: 1, PID: 42, Z: 1, Bounds: model.Rect{Width: 200, Height: 100}}, {ID: 2, PID: 42, Z: 2, Bounds: model.Rect{X: 300, Width: 200, Height: 100}}}
	w, ok := matchWindow(model.Rect{X: 310, Y: 20, Width: 30, Height: 10}, 42, windows)
	if !ok || w.ID != 2 {
		t.Fatalf("matched wrong same-PID window: %+v", w)
	}
}

func TestPopupCanEscapeFrameWithoutReassigningCoveredControls(t *testing.T) {
	frame := desktop.Window{ID: 1, PID: 42, Z: 0, Bounds: model.Rect{Width: 800, Height: 600}, Frame: model.Rect{Width: 800, Height: 600}}
	popup := desktop.Window{ID: 2, PID: 0, Z: 1, Bounds: model.Rect{X: 700, Y: 500, Width: 200, Height: 150}, Frame: model.Rect{X: 700, Y: 500, Width: 200, Height: 150}}
	windows := []desktop.Window{frame, popup}
	menuRect := model.Rect{X: 704, Y: 504, Width: 190, Height: 140}
	w, ok := windowFor(menuRect, "menu", "button", 42, 1, windows)
	if !ok || w.ID != 2 {
		t.Fatalf("menu inherited main frame instead of popup: %+v", w)
	}
	if got := visibleBounds(intersect(menuRect, w.Bounds), w.ID, w.Z, windows); area(got) != area(menuRect) {
		t.Fatalf("menu was clipped to its parent's X11 frame: %+v", got)
	}
	control := model.Rect{X: 720, Y: 520, Width: 30, Height: 20}
	w, ok = windowFor(control, "button", "group", 42, 1, windows)
	if !ok || w.ID != 1 {
		t.Fatalf("covered button reassigned to popup: %+v", w)
	}
	if area(visibleBounds(control, w.ID, w.Z, windows)) != 0 {
		t.Fatal("button behind popup stayed actionable")
	}
	// A submenu without an explicit menu ancestor can also lie entirely outside
	// its inherited frame, while remaining a child in the accessibility tree.
	w, ok = windowFor(model.Rect{X: 810, Y: 610, Width: 40, Height: 20}, "menu item", "button", 42, 1, windows)
	if !ok || w.ID != 2 {
		t.Fatalf("external submenu item lost: %+v", w)
	}
	popup.PID = 42
	windows[1] = popup
	w, ok = windowFor(control, "button", "group", 42, 1, windows)
	if !ok || w.ID != 1 {
		t.Fatalf("same-PID covered control reassigned: %+v", w)
	}
	w, ok = windowFor(control, "menu item", "toggle button", 42, 1, windows)
	if !ok || w.ID != 2 {
		t.Fatalf("direct GtkMenuButton menuitem lost: %+v", w)
	}
	w, ok = windowFor(control, "menu item", "menu bar", 42, 1, windows)
	if !ok || w.ID != 1 {
		t.Fatalf("covered menubar item reassigned to popup: %+v", w)
	}
	compactFrame := desktop.Window{ID: 3, PID: 42, Z: 0, Bounds: model.Rect{X: 500, Y: 368, Width: 280, Height: 100}}
	tallPopup := desktop.Window{ID: 4, PID: 0, Z: 1, Bounds: model.Rect{X: 513, Y: 418, Width: 167, Height: 382}}
	firstItem := model.Rect{X: 513, Y: 418, Width: 167, Height: 27}
	w, ok = windowFor(firstItem, "menu item", "toggle button", 42, 3, []desktop.Window{compactFrame, tallPopup})
	if !ok || w.ID != 4 {
		t.Fatalf("menu larger than main frame lost its first item: %+v", w)
	}
	w, ok = windowFor(firstItem, "menu item", "menu", 42, 3, []desktop.Window{compactFrame, tallPopup})
	if !ok || w.ID != 3 {
		t.Fatalf("ordinary menu child changed its existing owner: %+v", w)
	}
}

func TestStatesCross32BitBoundaryAndDoNotConfusePressedWithSelected(t *testing.T) {
	states := []uint32{1<<8 | 1<<24 | 1<<20 | 1<<23, 1<<1 | 1<<10}
	names := stateNames(states)
	for _, want := range []string{"pressed", "selected", "required", "has-popup"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s in %v", want, names)
		}
	}
	for _, n := range names {
		if n == "disabled" {
			t.Fatalf("enabled sensitive widget is disabled: %v", names)
		}
	}
	if stateHas(nil, 42) {
		t.Fatal("empty states set")
	}
}

func TestReferenceIDsAreStableAndUnambiguous(t *testing.T) {
	a := objectRef{Bus: ":1.20", Path: dbus.ObjectPath("/org/a11y/atspi/accessible/2")}
	b := a
	b.Bus = ":1.21"
	if nodeID(a) != nodeID(a) || nodeID(a) == nodeID(b) {
		t.Fatal("reference identity collision")
	}
}

func TestDBusWireSignatures(t *testing.T) {
	// These signatures are specified by AT-SPI2, not application-specific mocks.
	if got := dbus.SignatureOf([]objectRef{}).String(); got != "a(so)" {
		t.Fatalf("GetChildren: %s", got)
	}
	if got := dbus.SignatureOf([]actionInfo{}).String(); got != "a(sss)" {
		t.Fatalf("GetActions: %s", got)
	}
	if got := dbus.SignatureOf(struct{ X, Y, Width, Height int32 }{}).String(); got != "(iiii)" {
		t.Fatalf("GetExtents: %s", got)
	}
}

func TestUTF8BoundsAndProtectedRole(t *testing.T) {
	s := clean(strings.Repeat("漢", 100), 101)
	if !utf8.ValidString(s) || len(s) > 101 {
		t.Fatal("invalid truncated UTF-8")
	}
	if got := normalizeRole("password text", map[string]bool{}); got != "textbox" {
		t.Fatalf("password role %s", got)
	}
	if got := normalizeRole("check menu item", nil); got != "menuitemcheckbox" {
		t.Fatalf("menu role %s", got)
	}
	if !reflect.DeepEqual(normalizeRole("entry", nil), "textbox") {
		t.Fatal("entry role")
	}
	if got := normalizeRole("button", nil); got != "button" {
		t.Fatalf("GTK button role %s", got)
	}
}

func TestKeyBindingsAreExecutableChords(t *testing.T) {
	for source, want := range map[string]string{"N;Alt+F:N;Ctrl+N": "Ctrl+N", "f;;": "Alt+f", "<Control><Shift>s": "Ctrl+Shift+s", ";;": "", ";Alt+F:N;": "", "<Alt>o;;": "Alt+o"} {
		if got := keyBinding(source); got != want {
			t.Fatalf("%q: got %q want %q", source, got, want)
		}
	}
}
