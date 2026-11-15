package server

import (
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/calinrus-dev/semadesk/internal/model"
)

func (s *Server) pauseHTTP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Paused == nil {
		writeError(w, http.StatusBadRequest, "paused must be a boolean")
		return
	}
	s.mu.Lock()
	if s.control.Owner != "" {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "return manual control before changing agent pause")
		return
	}
	if s.control.Paused != *body.Paused {
		s.control.Paused = *body.Paused
		if s.control.Paused && s.actionCancel != nil {
			s.actionCancel()
		}
		s.broadcastLocked(map[string]any{"type": "control", "control": s.control})
	}
	control := s.control
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "control": control})
}

func (s *Server) agentStatusHTTP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var body struct {
		Phase string `json:"phase"`
		Label string `json:"label"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	switch body.Phase {
	case "idle", "working", "waiting", "done", "error":
	default:
		writeError(w, http.StatusBadRequest, "phase must be idle, working, waiting, done, or error")
		return
	}
	label := strings.TrimSpace(body.Label)
	if utf8.RuneCountInString(label) > 120 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		writeError(w, http.StatusBadRequest, "label must be single-line text with at most 120 characters")
		return
	}
	status := &model.AgentStatus{Phase: body.Phase, Label: label, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	s.mu.Lock()
	s.agentStatus = status
	s.broadcastLocked(map[string]any{"type": "agent_status", "status": status})
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}
