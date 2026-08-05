package session

import "testing"

func TestBufferLimit(t *testing.T) {
	b := NewBuffer(4)
	b.Append([]byte("abc"))
	b.Append([]byte("def"))
	p, n := b.Snapshot()
	if string(p) != "cdef" || n != 6 {
		t.Fatalf("got %q %d", p, n)
	}
	b.Clear()
	p, n = b.Snapshot()
	if len(p) != 0 || n != 0 {
		t.Fatal("clear failed")
	}
}
func TestBufferDelta(t *testing.T) {
	b := NewBuffer(4)
	b.Append([]byte("abc"))
	p, n, reset := b.Delta(1)
	if string(p) != "bc" || n != 3 || reset {
		t.Fatalf("got %q %d %v", p, n, reset)
	}
	b.Append([]byte("def"))
	p, n, reset = b.Delta(1)
	if string(p) != "cdef" || n != 6 || !reset {
		t.Fatalf("got %q %d %v", p, n, reset)
	}
}
