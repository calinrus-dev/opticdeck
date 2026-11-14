package desktop

import (
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func TestKeysym(t *testing.T) {
	for _, test := range []struct {
		key  string
		want uint32
	}{{"Enter", 0xff0d}, {"Ctrl", 0xffe3}, {"F1", 0xffbe}, {"F35", 0xffe0}, {"A", 65}, {"é", 233}, {"界", 0x0100754c}, {"Plus", 43}} {
		got, err := keysym(test.key)
		if err != nil || got != test.want {
			t.Errorf("keysym(%q)=%x,%v; want %x", test.key, got, err, test.want)
		}
	}
	for _, key := range []string{"", "does-not-exist", "\n", string([]byte{0xff})} {
		if _, err := keysym(key); err == nil {
			t.Errorf("accepted invalid key %q", key)
		}
	}
}

func TestPropertyWords(t *testing.T) {
	if got := uints(&xproto.GetPropertyReply{Format: 8, Value: []byte{1, 2, 3, 4}}); got != nil {
		t.Fatal("accepted wrong property format")
	}
	p := &xproto.GetPropertyReply{Format: 32, Value: make([]byte, 8)}
	xgb.Put32(p.Value, 0x12345678)
	xgb.Put32(p.Value[4:], 0xabcdef01)
	got := uints(p)
	if len(got) != 2 || got[0] != 0x12345678 || got[1] != 0xabcdef01 {
		t.Fatalf("bad words: %x", got)
	}
}

func TestOpenRequiresLocalExplicitDisplay(t *testing.T) {
	for _, display := range []string{"", "localhost:0", "host:1", " :1", ":1\n"} {
		if d, err := Open(display); err == nil {
			d.Close()
			t.Fatalf("accepted unsafe display %q", display)
		}
	}
}
