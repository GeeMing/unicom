//go:build windows
// +build windows

package terminalview

import (
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
	"github.com/lxn/win"
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

func TestScrollingToBottomResumesFrozenOutput(t *testing.T) {
	for _, offset := range []int{0, 2} {
		tv := newTestView(t, 12, 4)
		feedTestView(t, tv, strings.Repeat("old\r\n", 8))
		tv.setScrollOffset(offset)
		tv.selectAll()
		feedTestView(t, tv, "LATEST")
		if frozen, _ := tv.ViewStatus(); !frozen {
			t.Fatal("selection was not frozen")
		}
		notified := false
		tv.ViewChanged().Attach(func() {
			frozen, offset := tv.ViewStatus()
			notified = !frozen && offset == 0
		})
		tv.WndProc(0, win.WM_VSCROLL, win.SB_BOTTOM, 0)
		if !notified || tv.selectionSet || tv.snapshot != nil {
			t.Fatal("scrollbar bottom did not resume and notify the view")
		}
		if !strings.Contains(tv.Text(), "LATEST") {
			t.Fatal("background output was lost")
		}
	}
}

func TestDragAtBottomKeepsSnapshotUntilFinished(t *testing.T) {
	tv := newTestView(t, 12, 4)
	feedTestView(t, tv, "original")
	tv.selectAll()
	want := tv.selectedText()
	tv.selecting = true
	feedTestView(t, tv, "\rchanged")
	tv.scrollBy(-1)
	if tv.snapshot == nil || tv.selectedText() != want {
		t.Fatal("drag scrolling to the bottom released the selection")
	}
	tv.stopSelectionDrag()
	tv.scrollBy(-1)
	if frozen, offset := tv.ViewStatus(); frozen || offset != 0 {
		t.Fatal("scrolling after the drag did not resume output")
	}
}

func TestEscapeReturnsFromHistoryWithoutSendingToDevice(t *testing.T) {
	tv := newTestView(t, 12, 4)
	feedTestView(t, tv, strings.Repeat("old\r\n", 8))
	tv.setScrollOffset(2)
	var sent string
	tv.sendText = func(text string) error { sent += text; return nil }
	tv.WndProc(0, win.WM_CHAR, 27, 0)
	if sent != "" || tv.scrollOffset != 0 {
		t.Fatal("Escape should leave history without sending a character")
	}
	tv.WndProc(0, win.WM_CHAR, 27, 0)
	if sent != "\x1b" {
		t.Fatal("Escape in live mode should reach the device")
	}
}

func TestSelectAllIncludesHistoryAndFreezesIncomingOutput(t *testing.T) {
	tv := newTestView(t, 12, 4)
	feedTestView(t, tv, "first\r\nsecond\r\nthird\r\nfourth\r\nfifth")
	tv.selectAll()
	want := "first\r\nsecond\r\nthird\r\nfourth\r\nfifth"
	if got := tv.selectedText(); got != want {
		t.Fatalf("select all = %q, want %q", got, want)
	}
	feedTestView(t, tv, "\r\nnew output")
	if got := tv.selectedText(); got != want {
		t.Fatalf("incoming output changed select all: %q", got)
	}
	tv.ResumeLive()
	if frozen, offset := tv.ViewStatus(); frozen || offset != 0 || tv.selectionSet {
		t.Fatal("explicit resume did not clear selection")
	}
}
