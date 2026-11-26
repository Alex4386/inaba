package ptyterm

import "testing"

func TestLastTitle(t *testing.T) {
	for _, c := range []struct {
		in, title, partial string
		ok                 bool
	}{
		{"plain output", "", "", false},
		{"\x1b]0;orca@box: ~/aaa\x07$ ", "orca@box: ~/aaa", "", true},
		{"\x1b]2;one\x1b\\then \x1b]0;two\x07", "two", "", true},
		{"\x1b]0;done\x07 and \x1b]0;cut off", "done", "\x1b]0;cut off", true},
		{"\x1b]8;;http://x\x07link\x1b]8;;\x07", "", "", false},
	} {
		title, ok, partial := lastTitle([]byte(c.in))
		if title != c.title || ok != c.ok || string(partial) != c.partial {
			t.Errorf("lastTitle(%q) = %q %v %q", c.in, title, ok, partial)
		}
	}
	// A sequence cut by a read is finished by the next.
	_, _, partial := lastTitle([]byte("$ \x1b]0;ssh pro"))
	if title, ok, _ := lastTitle(append(partial, []byte("d\x07")...)); !ok || title != "ssh prod" {
		t.Errorf("a title across two reads = %q %v", title, ok)
	}
}
