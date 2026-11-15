package main

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/calinrus-dev/semadesk/internal/model"
)

type compactControl struct {
	ID      string   `json:"id"`
	Role    string   `json:"role"`
	Name    string   `json:"name,omitempty"`
	Actions []string `json:"actions,omitempty"`
	States  []string `json:"states,omitempty"`
	Bounds  []int    `json:"bounds,omitempty"`
}

func writeCompactTree(w io.Writer, raw json.RawMessage, match string, limit int) error {
	var snapshot model.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	controls := make([]compactControl, 0, limit)
	matched := 0
	match = strings.ToLower(strings.TrimSpace(match))
	for _, n := range snapshot.Nodes {
		if !compactRelevant(n) {
			continue
		}
		if match != "" && !strings.Contains(strings.ToLower(n.Name), match) && !strings.Contains(strings.ToLower(n.Role), match) {
			continue
		}
		matched++
		if len(controls) == limit {
			continue
		}
		c := compactControl{ID: n.ID, Role: n.Role, Name: n.Name, Actions: n.Actions}
		for _, state := range n.States {
			switch state {
			case "checked", "disabled", "expanded", "focused", "indeterminate", "protected", "read-only", "required", "selected":
				c.States = append(c.States, state)
			}
		}
		if n.VisionFallback {
			c.Bounds = []int{n.Bounds.X, n.Bounds.Y, n.Bounds.Width, n.Bounds.Height}
		}
		controls = append(controls, c)
	}
	return writeJSON(w, map[string]any{"revision": snapshot.Revision, "paused": snapshot.Control.Paused,
		"matched": matched, "controls": controls})
}

func compactRelevant(n model.Node) bool {
	if n.VisionFallback {
		return true
	}
	if len(n.Actions) == 0 {
		return false
	}
	switch n.Role {
	case "button", "cell", "checkbox", "combobox", "link", "listbox", "menu", "menuitem", "menuitemcheckbox", "menuitemradio", "option", "radio", "row", "scrollbar", "slider", "spinbutton", "tab", "textbox", "treeitem":
		return true
	}
	return false
}
