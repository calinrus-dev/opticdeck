package semantic

import (
	"strings"

	"github.com/calinrus-dev/semadesk/internal/desktop"
	"github.com/calinrus-dev/semadesk/internal/model"
)

func intersect(a, b model.Rect) model.Rect {
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	r, bottom := min(a.X+a.Width, b.X+b.Width), min(a.Y+a.Height, b.Y+b.Height)
	if r <= x || bottom <= y {
		return model.Rect{}
	}
	return model.Rect{X: x, Y: y, Width: r - x, Height: bottom - y}
}

func area(r model.Rect) int64 { return int64(max(0, r.Width)) * int64(max(0, r.Height)) }

// subtract preserves the visible portions around an occluding window.
func subtract(a, cover model.Rect) []model.Rect {
	i := intersect(a, cover)
	if area(i) == 0 {
		return []model.Rect{a}
	}
	out := []model.Rect{}
	add := func(r model.Rect) {
		if area(r) > 0 {
			out = append(out, r)
		}
	}
	add(model.Rect{X: a.X, Y: a.Y, Width: a.Width, Height: i.Y - a.Y})
	add(model.Rect{X: a.X, Y: i.Y + i.Height, Width: a.Width, Height: a.Y + a.Height - i.Y - i.Height})
	add(model.Rect{X: a.X, Y: i.Y, Width: i.X - a.X, Height: i.Height})
	add(model.Rect{X: i.X + i.Width, Y: i.Y, Width: a.X + a.Width - i.X - i.Width, Height: i.Height})
	return out
}

// A DOM element has one rectangle. Choosing the largest visible fragment is
// conservative: it cannot cover a foreground window or invent a hit target.
func visibleBounds(r model.Rect, window uint32, z int, windows []desktop.Window) model.Rect {
	parts := []model.Rect{r}
	for _, w := range windows {
		if w.ID == window || w.Z <= z {
			continue
		}
		cover := w.Frame
		if area(cover) == 0 {
			cover = w.Bounds
		}
		next := []model.Rect{}
		for _, p := range parts {
			next = append(next, subtract(p, cover)...)
		}
		parts = next
		if len(parts) > 128 {
			return model.Rect{}
		}
	}
	best := model.Rect{}
	for _, p := range parts {
		if area(p) > area(best) {
			best = p
		}
	}
	return best
}

func matchWindow(bounds model.Rect, pid uint32, windows []desktop.Window) (desktop.Window, bool) {
	var best desktop.Window
	bestScore := -1.0
	for _, w := range windows {
		overlap := area(intersect(bounds, w.Bounds))
		if overlap == 0 {
			continue
		}
		// Intersection-over-union distinguishes two same-PID windows instead of
		// assigning every widget to the most recently opened window.
		union := area(bounds) + area(w.Bounds) - overlap
		score := float64(overlap) / float64(max(int64(1), union))
		if pid != 0 && w.PID == pid {
			score += 2
		}
		if score > bestScore || (score == bestScore && w.Z > best.Z) {
			best, bestScore = w, score
		}
	}
	return best, bestScore >= 0
}

func ownsWindow(role string) bool {
	role = strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(strings.TrimSpace(role)))
	switch role {
	case "menu", "popup menu", "tool tip", "tooltip", "frame", "window", "dialog", "alert", "notification", "file chooser", "color chooser", "font chooser", "input method window":
		return true
	default:
		return false
	}
}

// Menus are accessible children of their parent frame but are often separate,
// override-redirect X11 windows with no _NET_WM_PID. Only these boundaries (or
// nodes entirely outside the inherited window) may cross the inherited owner.
// Ordinary controls preserve ownership so a same-PID popup covering a button
// cannot make the covered button appear to belong to the foreground popup.
func windowFor(bounds model.Rect, role, parentRole string, pid, inherited uint32, windows []desktop.Window) (desktop.Window, bool) {
	var parent desktop.Window
	hasParent := false
	for _, w := range windows {
		if w.ID == inherited {
			parent, hasParent = w, true
			break
		}
	}
	boundary := ownsWindow(role)
	// GTK may omit the intermediate GtkMenu accessible and attach popup items
	// directly to a GtkMenuButton. Menubar items remain owned by the main frame.
	menuItem := role == "menu item" || role == "check menu item" || role == "radio menu item"
	directPopupItem := menuItem && (parentRole == "button" || parentRole == "push button" || parentRole == "toggle button" || parentRole == "combo box" || parentRole == "frame" || parentRole == "window" || parentRole == "dialog")
	if directPopupItem {
		boundary = true
	}
	if hasParent && !boundary && area(intersect(bounds, parent.Bounds)) > 0 {
		return parent, true
	}
	if boundary || hasParent {
		var best desktop.Window
		score := -1.0
		for _, w := range windows {
			if pid != 0 && w.PID != 0 && w.PID != pid {
				continue
			}
			overlap := area(intersect(bounds, w.Bounds))
			if overlap == 0 {
				continue
			}
			union := area(bounds) + area(w.Bounds) - overlap
			s := float64(overlap) / float64(max(int64(1), union))
			if directPopupItem {
				// A menu can be larger than its parent frame. For its direct
				// items containment and stacking distinguish the mapped popup;
				// IoU alone would prefer the smaller main frame.
				s = float64(overlap) / float64(max(int64(1), area(bounds)))
			} else if pid != 0 && w.PID == pid {
				s += 0.05
			}
			if s > score || (s == score && w.Z > best.Z) {
				best, score = w, s
			}
		}
		if score >= 0 {
			return best, true
		}
	}
	if hasParent && area(intersect(bounds, parent.Bounds)) > 0 {
		return parent, true
	}
	return matchWindow(bounds, pid, windows)
}
