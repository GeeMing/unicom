// +build windows

package terminalview

import (
	"io"
	"testing"

	"github.com/hinshun/vt10x"
)

type testRWC struct{}

func (*testRWC) Read([]byte) (int, error)    { return 0, io.EOF }
func (*testRWC) Write(p []byte) (int, error) { return len(p), nil }
func (*testRWC) Close() error                { return nil }

func TestVTParser(t *testing.T) {
	state := new(vt10x.State)
	term, err := vt10x.Create(state, new(testRWC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = term.Write([]byte("ABC\x1b[2;3H\x1b[31mX")); err != nil {
		t.Fatal(err)
	}
	ch, fg, _ := state.Cell(2, 1)
	if ch != 'X' || fg != vt10x.Red {
		t.Fatalf("cell=%q fg=%d", ch, fg)
	}
}

func TestVTWideRune(t *testing.T) {
	state := new(vt10x.State)
	term, err := vt10x.Create(state, new(testRWC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = term.Write([]byte("中A")); err != nil {
		t.Fatal(err)
	}
	x, y := state.Cursor()
	if x != 3 || y != 0 {
		t.Fatalf("cursor=(%d,%d)", x, y)
	}
	ch, _, _ := state.Cell(2, 0)
	if ch != 'A' {
		t.Fatalf("cell 2=%q", ch)
	}
}
