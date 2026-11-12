//go:build windows
// +build windows

package terminalview

import (
	"io"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/hinshun/vt10x"
	"github.com/lxn/walk"
	"github.com/lxn/win"
)

const selectionScrollTimer = 1

type terminalIO struct{ write func([]byte) error }

func (t *terminalIO) Read([]byte) (int, error) { return 0, io.EOF }
func (t *terminalIO) Write(p []byte) (int, error) {
	if t.write == nil {
		return len(p), nil
	}
	if err := t.write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (t *terminalIO) Close() error { return nil }

type TerminalView struct {
	*walk.CustomWidget
	state         *vt10x.State
	vt            *vt10x.VT
	font          gridFont
	brushes       map[walk.Color]*walk.SolidColorBrush
	sendBytes     func([]byte) error
	sendText      func(string) error
	highSurrogate uint16
	// scrollOffset is the number of lines between the displayed viewport and
	// the live bottom. Zero means that new output is followed automatically.
	scrollOffset int
	selecting    bool
	selectionSet bool
	selectAnchor cellPosition
	selectFocus  cellPosition
	snapshot     *screenSnapshot
	dragMouse    uintptr
	dragScroll   int
	dragMoved    bool
	suppressChar uint16

	viewChangedPublisher                  walk.EventPublisher
	copyAction, pasteAction, latestAction *walk.Action
}

// cellPosition addresses a cell in the combined scrollback + live screen.
type cellPosition struct {
	row int
	col int
}

func New(parent walk.Container, sendBytes func([]byte) error, sendText func(string) error) (*TerminalView, error) {
	tv := &TerminalView{brushes: make(map[walk.Color]*walk.SolidColorBrush), sendBytes: sendBytes, sendText: sendText}
	cw, err := walk.NewCustomWidget(parent, win.WS_TABSTOP|win.WS_VSCROLL, func(canvas *walk.Canvas, update walk.Rectangle) error { return tv.paint(canvas, update) })
	if err != nil {
		return nil, err
	}
	tv.CustomWidget = cw
	if err = walk.InitWrapperWindow(tv); err != nil {
		return nil, err
	}
	tv.SetPaintMode(walk.PaintBuffered)
	tv.SetInvalidatesOnResize(true)
	tv.Disposing().Attach(tv.disposeResources)
	if err = tv.Reset(); err != nil {
		tv.Dispose()
		return nil, err
	}
	if err = tv.initContextMenu(); err != nil {
		tv.Dispose()
		return nil, err
	}
	return tv, nil
}

func (tv *TerminalView) LayoutFlags() walk.LayoutFlags {
	return walk.ShrinkableHorz | walk.ShrinkableVert | walk.GrowableHorz | walk.GrowableVert | walk.GreedyHorz | walk.GreedyVert
}
func (tv *TerminalView) MinSizeHint() walk.Size { return walk.Size{Width: 320, Height: 180} }
func (tv *TerminalView) SizeHint() walk.Size    { return walk.Size{Width: 720, Height: 432} }

// ViewStatus reports whether a selection has frozen the view and how far the
// viewport is above the bottom. Access it on the UI thread.
func (tv *TerminalView) ViewStatus() (frozen bool, historyOffset int) {
	return tv.snapshot != nil, tv.scrollOffset
}

func (tv *TerminalView) ViewChanged() *walk.Event {
	return tv.viewChangedPublisher.Event()
}

func (tv *TerminalView) Reset() error {
	state := new(vt10x.State)
	term, err := vt10x.Create(state, &terminalIO{write: tv.sendBytes})
	if err != nil {
		return err
	}
	tv.stopSelectionDrag()
	tv.snapshot = nil
	tv.state, tv.vt = state, term
	tv.scrollOffset = 0
	tv.selecting, tv.selectionSet = false, false
	tv.resizeTerminal()
	tv.updateScrollBar()
	return tv.Invalidate()
}

func (tv *TerminalView) Feed(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	oldHistory, oldDropped := tv.liveHistory()
	_, err := tv.vt.Write(p)
	if tv.snapshot == nil {
		newHistory, newDropped := tv.liveHistory()
		if tv.scrollOffset > 0 {
			tv.scrollOffset += newHistory - oldHistory + int(newDropped-oldDropped)
		}
		tv.clampScrollOffset(newHistory)
		tv.updateScrollBar()
		if tv.CustomWidget != nil {
			tv.Invalidate()
		}
	}
	return err
}

func (tv *TerminalView) Text() string {
	if tv.state == nil {
		return ""
	}
	return tv.state.String()
}

func (tv *TerminalView) resizeTerminal() {
	if tv.vt == nil || tv.Handle() == 0 {
		return
	}
	hdc := win.GetDC(tv.Handle())
	if hdc == 0 {
		return
	}
	err := tv.font.ensure(hdc, int(win.GetDeviceCaps(hdc, win.LOGPIXELSY)))
	win.ReleaseDC(tv.Handle(), hdc)
	if err != nil {
		return
	}
	b := tv.ClientBounds()
	cols, rows := maxInt(10, b.Width/tv.font.width), maxInt(4, b.Height/tv.font.height)
	tv.state.Lock()
	oldRows, oldCols := tv.state.Size()
	tv.state.Unlock()
	if cols != oldCols || rows != oldRows {
		// A real geometry change invalidates cell-based selection coordinates.
		tv.stopSelectionDrag()
		tv.snapshot, tv.selectionSet = nil, false
		tv.vt.Resize(cols, rows)
	}
	tv.clampScrollOffset(tv.historySize())
	tv.updateScrollBar()
}

func (tv *TerminalView) historySize() int {
	_, _, history := tv.viewSize()
	return history
}

func (tv *TerminalView) liveHistory() (int, int64) {
	tv.state.Lock()
	defer tv.state.Unlock()
	return tv.state.ScrollbackSize(), tv.state.ScrollbackDropped()
}

func (tv *TerminalView) clampScrollOffset(history int) {
	if tv.scrollOffset < 0 {
		tv.scrollOffset = 0
	}
	if tv.scrollOffset > history {
		tv.scrollOffset = history
	}
}

func (tv *TerminalView) updateScrollBar() {
	defer tv.viewChangedPublisher.Publish()
	if tv.CustomWidget == nil || tv.Handle() == 0 {
		return
	}
	rows, _, history := tv.viewSize()
	tv.clampScrollOffset(history)
	info := win.SCROLLINFO{
		CbSize: uint32(unsafe.Sizeof(win.SCROLLINFO{})),
		FMask:  win.SIF_RANGE | win.SIF_PAGE | win.SIF_POS | win.SIF_DISABLENOSCROLL,
		NMin:   0,
		NMax:   int32(history + rows - 1),
		NPage:  uint32(rows),
		NPos:   int32(history - tv.scrollOffset),
	}
	win.SetScrollInfo(tv.Handle(), win.SB_VERT, &info, true)
}

func (tv *TerminalView) setScrollOffset(offset int) {
	// Scrolling to the bottom resumes incoming output, except while an active
	// drag still needs its snapshot to keep selection coordinates stable.
	if offset <= 0 && !tv.selecting {
		tv.resumeLive()
		return
	}
	history := tv.historySize()
	tv.scrollOffset = offset
	tv.clampScrollOffset(history)
	tv.updateScrollBar()
	if tv.CustomWidget != nil {
		tv.Invalidate()
	}
}

func (tv *TerminalView) scrollBy(lines int) {
	tv.setScrollOffset(tv.scrollOffset + lines)
}

func orderedSelection(a, b cellPosition) (cellPosition, cellPosition) {
	if a.row > b.row || (a.row == b.row && a.col > b.col) {
		return b, a
	}
	return a, b
}

func (tv *TerminalView) cellSelected(row, col int) bool {
	if !tv.selectionSet {
		return false
	}
	start, end := orderedSelection(tv.selectAnchor, tv.selectFocus)
	p := cellPosition{row: row, col: col}
	return (p.row > start.row || (p.row == start.row && p.col >= start.col)) &&
		(p.row < end.row || (p.row == end.row && p.col <= end.col))
}

func (tv *TerminalView) positionFromMouse(lParam uintptr) cellPosition {
	x := int(int16(uint16(lParam & 0xffff)))
	y := int(int16(uint16((lParam >> 16) & 0xffff)))
	rows, cols, history := tv.viewSize()
	col := x / maxInt(1, tv.font.width)
	row := y / maxInt(1, tv.font.height)
	if x < 0 {
		col = 0
	}
	if y < 0 {
		row = 0
	}
	col = clampInt(col, 0, cols-1)
	row = clampInt(row, 0, rows-1)
	viewStart := history - tv.scrollOffset
	return cellPosition{row: clampInt(viewStart+row, 0, history+rows-1), col: col}
}

func (tv *TerminalView) selectedText() string {
	if tv.snapshot == nil || !tv.selectionSet {
		return ""
	}
	start, end := orderedSelection(tv.selectAnchor, tv.selectFocus)
	rows, cols, history := tv.viewSize()
	maxRow := history + rows - 1
	start.row = clampInt(start.row, 0, maxRow)
	end.row = clampInt(end.row, 0, maxRow)
	var result strings.Builder
	for row := start.row; row <= end.row; row++ {
		from, to := 0, cols-1
		if row == start.row {
			from = clampInt(start.col, 0, cols-1)
		}
		if row == end.row {
			to = clampInt(end.col, 0, cols-1)
		}
		// Include the lead glyph when a drag starts on its continuation cell.
		if from > 0 && tv.viewCell(from, row).ch == 0 {
			from--
		}
		line := make([]rune, 0, to-from+1)
		for col := from; col <= to; col++ {
			ch := tv.viewCell(col, row).ch
			if ch != 0 {
				line = append(line, ch)
			}
		}
		result.WriteString(strings.TrimRight(string(line), " \x00"))
		if row != end.row {
			result.WriteString("\r\n")
		}
	}
	return result.String()
}

func (tv *TerminalView) copySelection() bool {
	text := tv.selectedText()
	if text == "" {
		return false
	}
	return walk.Clipboard().SetText(text) == nil
}

func (tv *TerminalView) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	bounds := tv.ClientBounds()
	bg, err := tv.brush(walk.RGB(12, 14, 16))
	if err != nil {
		return err
	}
	if err = canvas.FillRectangle(bg, bounds); err != nil {
		return err
	}
	if tv.state == nil || tv.font.handle == 0 {
		return nil
	}
	rows, cols, history := tv.viewSize()
	tv.state.Lock()
	defer tv.state.Unlock()
	hdc := canvas.HDC()
	oldFont := win.SelectObject(hdc, win.HGDIOBJ(tv.font.handle))
	defer win.SelectObject(hdc, oldFont)
	oldMode := win.SetBkMode(hdc, win.TRANSPARENT)
	defer win.SetBkMode(hdc, oldMode)
	oldColor := win.SetTextColor(hdc, 0)
	defer win.SetTextColor(hdc, oldColor)
	tv.clampScrollOffset(history)
	viewStart := history - tv.scrollOffset
	cw, ch := tv.font.width, tv.font.height
	text := make([]uint16, 0, cols*2)
	advances := make([]int32, 0, cols*2)
	for y := 0; y < rows; y++ {
		row := viewStart + y
		for x := 0; x < cols; {
			start := x
			fg, cellBG := tv.cellColors(row, x, cols)
			text, advances = text[:0], advances[:0]
			for x < cols {
				f, b := tv.cellColors(row, x, cols)
				if f != fg || b != cellBG {
					break
				}
				c := tv.viewCell(x, row).ch
				width := 1
				if c != 0 && x+1 < cols && tv.viewCell(x+1, row).ch == 0 {
					width = 2
				}
				if c == 0 {
					c = ' '
				}
				text, advances = appendGlyph(text, advances, c, width*cw)
				x += width
			}
			brush, e := tv.brush(cellBG)
			if e != nil {
				return e
			}
			if e = canvas.FillRectangle(brush, walk.Rectangle{X: start * cw, Y: y * ch, Width: (x - start) * cw, Height: ch}); e != nil {
				return e
			}
			win.SetTextColor(hdc, win.COLORREF(fg))
			if e = drawGridRun(hdc, start*cw, y*ch, (x-start)*cw, ch, text, advances); e != nil {
				return e
			}
		}
	}
	if tv.snapshot == nil && tv.scrollOffset == 0 && tv.state.CursorVisible() && tv.Focused() {
		x, y := tv.state.Cursor()
		brush, e := tv.brush(walk.RGB(230, 233, 236))
		if e != nil {
			return e
		}
		return canvas.FillRectangle(brush, walk.Rectangle{X: x * cw, Y: y*ch + ch - 2, Width: cw, Height: 2})
	}
	return nil
}

func (tv *TerminalView) cellColors(row, col, cols int) (walk.Color, walk.Color) {
	c := tv.viewCell(col, row)
	selected := tv.cellSelected(row, col)
	if col+1 < cols && tv.viewCell(col+1, row).ch == 0 {
		selected = selected || tv.cellSelected(row, col+1)
	}
	if selected {
		return walk.RGB(255, 255, 255), walk.RGB(38, 92, 150)
	}
	return colorFor(c.fg, false), colorFor(c.bg, true)
}

func (tv *TerminalView) brush(color walk.Color) (*walk.SolidColorBrush, error) {
	if brush := tv.brushes[color]; brush != nil {
		return brush, nil
	}
	brush, err := walk.NewSolidColorBrush(color)
	if err != nil {
		return nil, err
	}
	tv.brushes[color] = brush
	return brush, nil
}

func (tv *TerminalView) disposeResources() {
	tv.stopSelectionDrag()
	tv.font.dispose()
	tv.snapshot = nil
	if menu := tv.ContextMenu(); menu != nil {
		menu.Actions().Clear()
		menu.Dispose()
		tv.SetContextMenu(nil)
	}
	for color, brush := range tv.brushes {
		brush.Dispose()
		delete(tv.brushes, color)
	}
}

func (tv *TerminalView) sendSpecial(key uintptr) bool {
	var seq string
	switch key {
	case win.VK_UP:
		seq = tv.cursorSequence("A")
	case win.VK_DOWN:
		seq = tv.cursorSequence("B")
	case win.VK_RIGHT:
		seq = tv.cursorSequence("C")
	case win.VK_LEFT:
		seq = tv.cursorSequence("D")
	case win.VK_HOME:
		seq = "\x1b[H"
	case win.VK_END:
		seq = "\x1b[F"
	case win.VK_INSERT:
		seq = "\x1b[2~"
	case win.VK_DELETE:
		seq = "\x1b[3~"
	case win.VK_PRIOR:
		seq = "\x1b[5~"
	case win.VK_NEXT:
		seq = "\x1b[6~"
	case win.VK_F1:
		seq = "\x1bOP"
	case win.VK_F2:
		seq = "\x1bOQ"
	case win.VK_F3:
		seq = "\x1bOR"
	case win.VK_F4:
		seq = "\x1bOS"
	case win.VK_F5:
		seq = "\x1b[15~"
	case win.VK_F6:
		seq = "\x1b[17~"
	case win.VK_F7:
		seq = "\x1b[18~"
	case win.VK_F8:
		seq = "\x1b[19~"
	case win.VK_F9:
		seq = "\x1b[20~"
	case win.VK_F10:
		seq = "\x1b[21~"
	case win.VK_F11:
		seq = "\x1b[23~"
	case win.VK_F12:
		seq = "\x1b[24~"
	default:
		return false
	}
	tv.resumeLive()
	if tv.sendBytes != nil {
		_ = tv.sendBytes([]byte(seq))
	}
	return true
}

func (tv *TerminalView) cursorSequence(final string) string {
	appCursor := false
	if tv.state != nil {
		tv.state.Lock()
		appCursor = tv.state.Mode(vt10x.ModeAppCursor)
		tv.state.Unlock()
	}
	if appCursor {
		return "\x1bO" + final
	}
	return "\x1b[" + final
}

func (tv *TerminalView) paste() bool {
	text, err := walk.Clipboard().Text()
	if err != nil || text == "" {
		return false
	}
	tv.resumeLive()
	if tv.sendText != nil {
		_ = tv.sendText(text)
	}
	return true
}

func (tv *TerminalView) sendUTF16Unit(unit uint16) {
	if unit >= 0xD800 && unit <= 0xDBFF {
		tv.highSurrogate = unit
		return
	}
	var r rune
	if unit >= 0xDC00 && unit <= 0xDFFF && tv.highSurrogate != 0 {
		r = utf16.DecodeRune(rune(tv.highSurrogate), rune(unit))
		tv.highSurrogate = 0
	} else {
		tv.highSurrogate = 0
		r = rune(unit)
	}
	if tv.sendText != nil {
		_ = tv.sendText(string(r))
	}
}

func (tv *TerminalView) WndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_GETDLGCODE:
		return win.DLGC_WANTALLKEYS | win.DLGC_WANTARROWS | win.DLGC_WANTCHARS | win.DLGC_WANTTAB
	case win.WM_LBUTTONDOWN:
		tv.SetFocus()
		tv.captureSelection()
		tv.selectAnchor = tv.positionFromMouse(lParam)
		tv.selectFocus = tv.selectAnchor
		tv.dragMouse = lParam
		tv.dragMoved = false
		tv.selecting, tv.selectionSet = true, true
		tv.viewChangedPublisher.Publish()
		win.SetCapture(hwnd)
		tv.Invalidate()
		return 0
	case win.WM_MOUSEMOVE:
		if tv.selecting && wParam&win.MK_LBUTTON != 0 {
			tv.dragMoved = tv.dragMoved || tv.dragMouse != lParam
			tv.dragMouse = lParam
			y := int(int16(uint16(lParam >> 16)))
			direction := 0
			if y < 0 {
				direction = 1
			} else if y >= tv.ClientBounds().Height {
				direction = -1
			}
			if direction != tv.dragScroll {
				tv.dragScroll = direction
				if direction == 0 {
					win.KillTimer(hwnd, selectionScrollTimer)
				} else {
					win.SetTimer(hwnd, selectionScrollTimer, 50, 0)
				}
			}
			if focus := tv.positionFromMouse(lParam); focus != tv.selectFocus {
				tv.selectFocus = focus
				tv.Invalidate()
			}
		}
		return 0
	case win.WM_TIMER:
		if wParam == selectionScrollTimer {
			if tv.selecting && tv.dragScroll != 0 {
				tv.scrollBy(tv.dragScroll)
				tv.selectFocus = tv.positionFromMouse(tv.dragMouse)
			}
			return 0
		}
	case win.WM_LBUTTONUP:
		if tv.selecting {
			tv.selectFocus = tv.positionFromMouse(lParam)
			tv.stopSelectionDrag()
			if !tv.dragMoved && tv.selectAnchor == tv.selectFocus {
				tv.resumeLive()
			}
			tv.Invalidate()
		}
		return 0
	case win.WM_CAPTURECHANGED, win.WM_CANCELMODE:
		tv.stopSelectionDrag()
	case win.WM_CONTEXTMENU:
		tv.SetFocus()
		tv.updateContextMenu()
		// The keyboard context-menu key supplies (-1, -1). Anchor its menu
		// inside the terminal instead of passing an off-screen position.
		if int32(lParam) == -1 {
			point := win.POINT{X: int32(tv.font.width), Y: int32(tv.font.height)}
			win.ClientToScreen(hwnd, &point)
			lParam = uintptr(uint16(point.X)) | uintptr(uint16(point.Y))<<16
		}
	case win.WM_SETFOCUS, win.WM_KILLFOCUS:
		tv.Invalidate()
	case win.WM_SIZE:
		tv.resizeTerminal()
	case win.WM_MOUSEWHEEL:
		delta := int16(uint16(wParam >> 16))
		if delta != 0 {
			lines := int(delta) / 120 * 3
			if lines == 0 {
				if delta > 0 {
					lines = 1
				} else {
					lines = -1
				}
			}
			tv.scrollBy(lines)
		}
		return 0
	case win.WM_VSCROLL:
		command := uint16(wParam & 0xffff)
		rows, _, _ := tv.viewSize()
		switch command {
		case win.SB_LINEUP:
			tv.scrollBy(1)
		case win.SB_LINEDOWN:
			tv.scrollBy(-1)
		case win.SB_PAGEUP:
			tv.scrollBy(maxInt(1, rows-1))
		case win.SB_PAGEDOWN:
			tv.scrollBy(-maxInt(1, rows-1))
		case win.SB_TOP:
			tv.setScrollOffset(tv.historySize())
		case win.SB_BOTTOM:
			tv.setScrollOffset(0)
		case win.SB_THUMBPOSITION, win.SB_THUMBTRACK:
			info := win.SCROLLINFO{CbSize: uint32(unsafe.Sizeof(win.SCROLLINFO{})), FMask: win.SIF_TRACKPOS}
			if win.GetScrollInfo(tv.Handle(), win.SB_VERT, &info) {
				tv.setScrollOffset(tv.historySize() - int(info.NTrackPos))
			}
		}
		return 0
	case win.WM_CHAR:
		if tv.suppressChar != 0 {
			unit := tv.suppressChar
			tv.suppressChar = 0
			if uint16(wParam) == unit {
				return 0
			}
		}
		if wParam == 27 && (tv.snapshot != nil || tv.scrollOffset != 0) {
			tv.resumeLive()
			return 0
		}
		// In terminal mode Ctrl+C normally sends ETX.  When text is selected it
		// follows the standard Windows convention and copies instead.
		if wParam == 3 && tv.selectionSet {
			tv.copySelection()
			return 0
		}
		tv.resumeLive()
		tv.sendUTF16Unit(uint16(wParam))
		return 0
	case win.WM_KEYDOWN:
		tv.suppressChar = 0
		if (wParam == uintptr(walk.KeyC) && walk.ControlDown() && walk.ShiftDown()) ||
			(wParam == win.VK_INSERT && walk.ControlDown()) {
			tv.copySelection()
			if wParam == uintptr(walk.KeyC) {
				tv.suppressChar = 3
			}
			return 0
		}
		if walk.ShiftDown() && (wParam == win.VK_PRIOR || wParam == win.VK_NEXT) {
			rows, _, _ := tv.viewSize()
			lines := maxInt(1, rows-1)
			if wParam == win.VK_NEXT {
				lines = -lines
			}
			tv.scrollBy(lines)
			return 0
		}
		if (wParam == win.VK_INSERT && walk.ShiftDown()) || (wParam == uintptr(walk.KeyV) && walk.ControlDown() && walk.ShiftDown()) {
			tv.paste()
			if wParam == uintptr(walk.KeyV) {
				tv.suppressChar = 22
			}
			return 0
		}
		if tv.sendSpecial(wParam) {
			return 0
		}
	}
	return tv.CustomWidget.WndProc(hwnd, msg, wParam, lParam)
}

func (tv *TerminalView) stopSelectionDrag() {
	tv.selecting, tv.dragScroll = false, 0
	if tv.CustomWidget != nil && tv.Handle() != 0 {
		win.KillTimer(tv.Handle(), selectionScrollTimer)
		captured, _, _ := getCapture.Call()
		if win.HWND(captured) == tv.Handle() {
			win.ReleaseCapture()
		}
	}
}

func (tv *TerminalView) resumeLive() {
	if tv.snapshot == nil && tv.scrollOffset == 0 {
		return
	}
	tv.stopSelectionDrag()
	tv.snapshot, tv.selectionSet = nil, false
	tv.scrollOffset = 0
	tv.updateScrollBar()
	if tv.CustomWidget != nil {
		tv.Invalidate()
	}
}

// ResumeLive releases any selection and follows the latest received output.
func (tv *TerminalView) ResumeLive() {
	tv.resumeLive()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

var ansiColors = [16]walk.Color{
	walk.RGB(0, 0, 0), walk.RGB(205, 49, 49), walk.RGB(13, 188, 121), walk.RGB(229, 229, 16),
	walk.RGB(36, 114, 200), walk.RGB(188, 63, 188), walk.RGB(17, 168, 205), walk.RGB(229, 229, 229),
	walk.RGB(102, 102, 102), walk.RGB(241, 76, 76), walk.RGB(35, 209, 139), walk.RGB(245, 245, 67),
	walk.RGB(59, 142, 234), walk.RGB(214, 112, 214), walk.RGB(41, 184, 219), walk.RGB(255, 255, 255),
}

func colorFor(color vt10x.Color, background bool) walk.Color {
	if color == vt10x.DefaultFG {
		return walk.RGB(214, 219, 224)
	}
	if color == vt10x.DefaultBG {
		return walk.RGB(12, 14, 16)
	}
	if color < 16 {
		return ansiColors[int(color)]
	}
	if color >= 16 && color <= 231 {
		n := int(color) - 16
		r, g, b := n/36, (n/6)%6, n%6
		level := func(v int) byte {
			if v == 0 {
				return 0
			}
			return byte(55 + v*40)
		}
		return walk.RGB(level(r), level(g), level(b))
	}
	if color >= 232 && color <= 255 {
		v := byte(8 + (int(color)-232)*10)
		return walk.RGB(v, v, v)
	}
	if background {
		return walk.RGB(12, 14, 16)
	}
	return walk.RGB(214, 219, 224)
}
