//go:build windows
// +build windows

package terminalview

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

func newTestView(t *testing.T, cols, rows int) *TerminalView {
	state := new(vt10x.State)
	vt, err := vt10x.Create(state, new(testRWC))
	if err != nil {
		t.Fatal(err)
	}
	vt.Resize(cols, rows)
	return &TerminalView{state: state, vt: vt, font: gridFont{width: 8, height: 18}}
}

func feedTestView(t *testing.T, tv *TerminalView, text string) {
	if err := tv.Feed([]byte(text)); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionCopyWideCharacters(t *testing.T) {
	tv := newTestView(t, 16, 4)
	feedTestView(t, tv, "A中文B\r\nsecond")
	tv.captureSelection()
	tv.selectionSet = true
	for _, tc := range []struct {
		name string
		a, b cellPosition
		want string
	}{
		{"whole", cellPosition{0, 0}, cellPosition{0, 5}, "A中文B"},
		{"continuation", cellPosition{0, 2}, cellPosition{0, 2}, "中"},
		{"lead", cellPosition{0, 1}, cellPosition{0, 1}, "中"},
		{"reverse", cellPosition{0, 4}, cellPosition{0, 2}, "中文"},
		{"multiline", cellPosition{0, 3}, cellPosition{1, 2}, "文B\r\nsec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tv.selectAnchor, tv.selectFocus = tc.a, tc.b
			if got := tv.selectedText(); got != tc.want {
				t.Fatalf("copy = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectionSnapshotSurvivesOutputAndHistoryEviction(t *testing.T) {
	tv := newTestView(t, 12, 4)
	feedTestView(t, tv, strings.Repeat("old\r\n", vt10x.MaxScrollbackLines+4)+"TARGET")
	tv.captureSelection()
	tv.selectionSet = true
	row := tv.snapshot.history + 3
	tv.selectAnchor, tv.selectFocus = cellPosition{row, 0}, cellPosition{row, 5}
	point := uintptr((3*18+2)<<16 | 2*8)
	before := tv.positionFromMouse(point)
	feedTestView(t, tv, "\rCHANGE"+strings.Repeat("\r\nnew", 25))
	if got := tv.selectedText(); got != "TARGET" {
		t.Fatalf("selection changed during output: %q", got)
	}
	if got := tv.positionFromMouse(point); got != before {
		t.Fatalf("mouse coordinates moved: %v -> %v", before, got)
	}
	if tv.scrollOffset != 0 {
		t.Fatalf("snapshot viewport moved: %d", tv.scrollOffset)
	}
	if got := tv.state.ScrollbackDropped(); got < 25 {
		t.Fatalf("test did not evict history: %d", got)
	}
	tv.resumeLive()
	if tv.snapshot != nil || tv.selectionSet || tv.scrollOffset != 0 {
		t.Fatal("resume did not release the snapshot and selection")
	}
	if !strings.Contains(tv.Text(), "new") {
		t.Fatal("incoming output was lost while selecting")
	}
}

func TestScrolledViewportTracksEvictions(t *testing.T) {
	tv := newTestView(t, 12, 4)
	feedTestView(t, tv, strings.Repeat("old\r\n", vt10x.MaxScrollbackLines+4))
	tv.scrollOffset = 10
	feedTestView(t, tv, strings.Repeat("new\r\n", 7))
	if tv.scrollOffset != 17 {
		t.Fatalf("scrolled viewport moved at full history: offset = %d", tv.scrollOffset)
	}
}

func TestSelectionFreezesHistoryWhileDraggingUp(t *testing.T) {
	tv := newTestView(t, 12, 4)
	feedTestView(t, tv, "first\r\nsecond\r\nthird\r\nfourth\r\nfifth")
	tv.captureSelection()
	tv.selectionSet = true
	tv.selectAnchor, tv.selectFocus = cellPosition{0, 0}, cellPosition{1, 5}
	feedTestView(t, tv, "\x1b[2J\x1b[Hreplacement")
	if got := tv.selectedText(); got != "first\r\nsecond" {
		t.Fatalf("history/live-screen selection changed: %q", got)
	}
	tv.scrollOffset = tv.historySize()
	if got := tv.positionFromMouse(0); got != (cellPosition{0, 0}) {
		t.Fatalf("top of snapshot = %v", got)
	}
}
