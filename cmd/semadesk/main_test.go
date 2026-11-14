package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirectoryRejectsSymlinkAndPublicDirectory(t *testing.T) {
	base := t.TempDir()
	public := filepath.Join(base, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if privateDir(public) == nil {
		t.Fatal("accepted public state directory")
	}
	private := filepath.Join(base, "private")
	if err := privateDir(private); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if privateDir(link) == nil {
		t.Fatal("accepted symlink")
	}
}

func TestStateURLMustStayOnLoopback(t *testing.T) {
	for _, u := range []string{"http://localhost:1234", "http://127.0.0.1:0", "http://127.0.0.1:80@evil.test", "http://127.0.0.1:8000/?token=secret", "https://127.0.0.1:8000", "http://127.0.0.1:8000/redirect"} {
		if validateURL(u) == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	if err := validateURL("http://127.0.0.1:8000"); err != nil {
		t.Fatal(err)
	}
}

func TestOptionsBoundResources(t *testing.T) {
	for _, args := range [][]string{{"--width", "8193"}, {"--height", "1"}, {"--port", "65536"}} {
		if _, err := options(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestGuardianCleanupRejectsUnrelatedDirectories(t *testing.T) {
	base := t.TempDir()
	if cleanRuntime(base) == nil {
		t.Fatal("accepted unrelated private directory")
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatal("unrelated directory removed")
	}
	path, err := os.MkdirTemp("", "semadesk-session-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	if err = os.WriteFile(filepath.Join(path, "cookie"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = cleanRuntime(path); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("private runtime was not cleaned")
	}
}
