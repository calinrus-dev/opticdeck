package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestHotkeyActivationIsScopedAndStopDoesNotWaitForPause(t *testing.T) {
	const owner = ":1.20"
	const session = dbus.ObjectPath("/org/freedesktop/portal/desktop/session/1_21/opticdeck_hotkeys")
	pauseRequest := make(chan bool, 4)
	stopRequest := make(chan struct{}, 1)
	finishPause := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/status":
			_, _ = w.Write([]byte(`{"control":{"paused":false}}`))
		case "/api/control":
			var body struct{ Paused bool }
			_ = json.NewDecoder(r.Body).Decode(&body)
			pauseRequest <- body.Paused
			<-finishPause
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/stop":
			stopRequest <- struct{}{}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	defer close(finishPause)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	signals := make(chan *dbus.Signal, 8)
	result := make(chan error, 1)
	go func() {
		result <- watchHotkeys(ctx, ctx, signals, owner, session, map[string]bool{pauseShortcut: true, stopShortcut: true}, state{URL: api.URL, Token: "fixture-secret"})
	}()
	activated := func(sender, id string, path dbus.ObjectPath) *dbus.Signal {
		return &dbus.Signal{Sender: sender, Path: shortcutServer, Name: shortcutIface + ".Activated",
			Body: []any{path, id, uint64(1), map[string]dbus.Variant{}}}
	}
	signals <- activated(":1.30", stopShortcut, session)
	signals <- activated(owner, stopShortcut, "/org/freedesktop/portal/desktop/session/1_21/other")
	signals <- activated(owner, "unknown", session)
	signals <- activated(owner, pauseShortcut, session)
	select {
	case paused := <-pauseRequest:
		if !paused {
			t.Fatal("running agent was not paused")
		}
	case <-ctx.Done():
		t.Fatal("pause shortcut did not send its API request")
	case <-stopRequest:
		t.Fatal("stop was accepted from an unrelated signal")
	}
	signals <- activated(owner, pauseShortcut, session)
	signals <- activated(owner, stopShortcut, session)
	select {
	case <-stopRequest:
	case <-ctx.Done():
		t.Fatal("stop waited for the in-flight pause request")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case <-pauseRequest:
		t.Fatal("pause shortcut handled repeat activation")
	default:
	}
}
