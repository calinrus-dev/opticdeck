package desktop

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jezek/xgb/xproto"
)

var namedKeys = map[string]uint32{
	"return": 0xff0d, "enter": 0xff0d, "escape": 0xff1b, "esc": 0xff1b,
	"tab": 0xff09, "backspace": 0xff08, "delete": 0xffff, "del": 0xffff,
	"insert": 0xff63, "home": 0xff50, "end": 0xff57, "pageup": 0xff55, "pagedown": 0xff56,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
	"arrowleft": 0xff51, "arrowup": 0xff52, "arrowright": 0xff53, "arrowdown": 0xff54,
	"space": 0x20, "plus": 0x2b, "minus": 0x2d,
	"shift": 0xffe1, "shift_l": 0xffe1, "shift_r": 0xffe2,
	"ctrl": 0xffe3, "control": 0xffe3, "control_l": 0xffe3, "control_r": 0xffe4,
	"alt": 0xffe9, "alt_l": 0xffe9, "alt_r": 0xffea,
	"super": 0xffeb, "meta": 0xffeb, "super_l": 0xffeb, "super_r": 0xffec,
	"capslock": 0xffe5, "numlock": 0xff7f, "print": 0xff61, "pause": 0xff13, "menu": 0xff67,
}

func keysym(key string) (uint32, error) {
	if v, ok := namedKeys[strings.ToLower(key)]; ok {
		return v, nil
	}
	if len(key) >= 2 && (key[0] == 'F' || key[0] == 'f') {
		n, err := strconv.Atoi(key[1:])
		if err == nil && n >= 1 && n <= 35 {
			return uint32(0xffbd + n), nil
		}
	}
	if !utf8.ValidString(key) || utf8.RuneCountInString(key) != 1 {
		return 0, errors.New("unsupported key name; use one Unicode character or a supported named key")
	}
	r, _ := utf8.DecodeRuneInString(key)
	if r < 0x20 || r == 0x7f {
		return 0, errors.New("use a named key for control characters")
	}
	if r <= 0xff {
		return uint32(r), nil
	}
	return 0x01000000 | uint32(r), nil
}

type mappedKey struct {
	code  xproto.Keycode
	shift bool
}

func (d *Desktop) keymap() (map[uint32]mappedKey, error) {
	s := xproto.Setup(d.conn)
	r, err := xproto.GetKeyboardMapping(d.conn, s.MinKeycode, byte(int(s.MaxKeycode)-int(s.MinKeycode)+1)).Reply()
	if err != nil {
		return nil, err
	}
	per := int(r.KeysymsPerKeycode)
	if per < 1 {
		return nil, errors.New("X11 keyboard mapping is empty")
	}
	result := make(map[uint32]mappedKey)
	for i := 0; i < len(r.Keysyms); i += per {
		for column := 0; column < per && column < 2; column++ {
			v := uint32(r.Keysyms[i+column])
			if v == 0 {
				continue
			}
			if _, exists := result[v]; !exists {
				result[v] = mappedKey{xproto.Keycode(int(s.MinKeycode) + i/per), column == 1}
			}
		}
	}
	return result, nil
}

func (d *Desktop) PressKey(key string) error { return d.shortcut([]string{key}) }

func (d *Desktop) Shortcut(chord string) error {
	if len(chord) > 256 {
		return errors.New("shortcut is too long")
	}
	parts := strings.Split(chord, "+")
	if len(parts) > 8 {
		return errors.New("too many shortcut modifiers")
	}
	return d.shortcut(parts)
}

func (d *Desktop) shortcut(parts []string) error {
	if len(parts) == 0 {
		return errors.New("empty shortcut")
	}
	d.inputMu.Lock()
	defer d.inputMu.Unlock()
	mapping, err := d.keymap()
	if err != nil {
		return err
	}
	var codes []xproto.Keycode
	seen := make(map[xproto.Keycode]bool)
	appendCode := func(c xproto.Keycode) {
		if !seen[c] {
			codes = append(codes, c)
			seen[c] = true
		}
	}
	for i, part := range parts {
		key := strings.TrimSpace(part)
		if part == " " {
			key = "Space"
		}
		if key == "" {
			return errors.New("empty shortcut component; use Plus for the plus key")
		}
		v, err := keysym(key)
		if err != nil {
			return err
		}
		if i < len(parts)-1 && v != 0xffe1 && v != 0xffe2 && v != 0xffe3 && v != 0xffe4 && v != 0xffe9 && v != 0xffea && v != 0xffeb && v != 0xffec {
			return errors.New("shortcut components before the final key must be modifiers")
		}
		m, ok := mapping[v]
		if !ok {
			return errors.New("key is not in the virtual X11 keyboard map; use set_text/clipboard for text")
		}
		if m.shift {
			shift, ok := mapping[0xffe1]
			if !ok {
				return errors.New("virtual keyboard has no Shift key")
			}
			appendCode(shift.code)
		}
		appendCode(m.code)
	}
	var pressed []xproto.Keycode
	defer func() {
		for i := len(pressed) - 1; i >= 0; i-- {
			_ = d.fake(xproto.KeyRelease, byte(pressed[i]), 0, 0)
		}
	}()
	for _, code := range codes {
		pressed = append(pressed, code)
		if err := d.fake(xproto.KeyPress, byte(code), 0, 0); err != nil {
			return err
		}
	}
	for len(pressed) > 0 {
		i := len(pressed) - 1
		if err := d.fake(xproto.KeyRelease, byte(pressed[i]), 0, 0); err != nil {
			return err
		}
		pressed = pressed[:i]
	}
	return nil
}
