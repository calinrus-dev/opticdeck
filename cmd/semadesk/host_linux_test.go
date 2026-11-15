package main

import (
	"slices"
	"testing"
)

func TestHostBackendDoesNotImplicitlyShareX11(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		x11                                         bool
		kind, display, wayland, waylandSocket, want string
	}{
		{name: "local Wayland", kind: "wayland", display: ":0", wayland: "wayland-0", want: "w0vncserver"},
		{name: "explicit native X11", x11: true, kind: "x11", display: ":1.0", want: "x0vncserver"},
		{name: "implicit X11", kind: "x11", display: ":0"},
		{name: "Xwayland", x11: true, kind: "x11", display: ":0", wayland: "wayland-0"},
		{name: "inherited Wayland socket", x11: true, kind: "x11", display: ":0", waylandSocket: "3"},
		{name: "network DISPLAY", x11: true, kind: "x11", display: "other-host:0"},
		{name: "forwarded DISPLAY", x11: true, kind: "x11", display: "localhost:10.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, selected, err := hostBackend(tc.x11, tc.kind, tc.display, tc.wayland, tc.waylandSocket)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("hostBackend returned %q, %v, want %q", got, err, tc.want)
			}
			if got == "x0vncserver" && selected != tc.display {
				t.Fatalf("backend selected %q rather than the session DISPLAY %q", selected, tc.display)
			}
		})
	}
}

func TestHostX11BackendRequiresConsentAndDisablesUnneededFeatures(t *testing.T) {
	args := hostBackendArgs("/tmp/fixture/rfb.sock", ":1.0", false)
	for _, required := range [][]string{
		{"-rfbport", "-1"}, {"-rfbunixpath", "/tmp/fixture/rfb.sock"}, {"-rfbunixmode", "0600"},
		{"-display", ":1.0"}, {"-QueryConnect"}, {"-AcceptSetDesktopSize=0"},
		{"-AcceptCutText=0"}, {"-SendCutText=0"}, {"-AcceptKeyEvents=0"}, {"-AcceptPointerEvents=0"},
	} {
		index := slices.Index(args, required[0])
		if index < 0 || index+len(required) > len(args) || !slices.Equal(args[index:index+len(required)], required) {
			t.Errorf("missing backend restriction %q", required)
		}
	}
}
