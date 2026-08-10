// +build windows

package terminalview

import (
	"io"
	"strings"
	"unicode/utf16"

	"github.com/hinshun/vt10x"
	"github.com/lxn/walk"
	"github.com/lxn/win"
)

const (
	cellWidth  = 9
	cellHeight = 18
)

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
	font          *walk.Font
	brushes       map[walk.Color]*walk.SolidColorBrush
	sendBytes     func([]byte) error
	sendText      func(string) error
	highSurrogate uint16
}

func New(parent walk.Container, sendBytes func([]byte) error, sendText func(string) error) (*TerminalView, error) {
	tv := &TerminalView{brushes: make(map[walk.Color]*walk.SolidColorBrush), sendBytes: sendBytes, sendText: sendText}
	font, err := walk.NewFont("Consolas", 11, 0)
	if err != nil {
		return nil, err
	}
	tv.font = font
	cw, err := walk.NewCustomWidget(parent, win.WS_TABSTOP, func(canvas *walk.Canvas, update walk.Rectangle) error { return tv.paint(canvas, update) })
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
		return nil, err
	}
	return tv, nil
}

func (tv *TerminalView) LayoutFlags() walk.LayoutFlags {
	return walk.ShrinkableHorz | walk.ShrinkableVert | walk.GrowableHorz | walk.GrowableVert | walk.GreedyHorz | walk.GreedyVert
}
func (tv *TerminalView) MinSizeHint() walk.Size { return walk.Size{Width: 320, Height: 180} }
func (tv *TerminalView) SizeHint() walk.Size    { return walk.Size{Width: 720, Height: 432} }

func (tv *TerminalView) Reset() error {
	state := new(vt10x.State)
	term, err := vt10x.Create(state, &terminalIO{write: tv.sendBytes})
	if err != nil {
		return err
	}
	tv.state, tv.vt = state, term
	tv.resizeTerminal()
	return tv.Invalidate()
}

func (tv *TerminalView) Feed(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	_, err := tv.vt.Write(p)
	tv.Invalidate()
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
	b := tv.ClientBounds()
	cols, rows := b.Width/cellWidth, b.Height/cellHeight
	if cols < 10 {
		cols = 10
	}
	if rows < 4 {
		rows = 4
	}
	tv.vt.Resize(cols, rows)
}

func (tv *TerminalView) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	tv.resizeTerminal()
	bounds := tv.ClientBounds()
	bg, err := tv.brush(walk.RGB(12, 14, 16))
	if err != nil {
		return err
	}
	if err = canvas.FillRectangle(bg, bounds); err != nil {
		return err
	}
	if tv.state == nil {
		return nil
	}

	tv.state.Lock()
	defer tv.state.Unlock()
	rows, cols := tv.state.Size()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; {
			ch, fg, cellBG := tv.state.Cell(x, y)
			start := x
			chars := make([]rune, 0, 16)
			for x < cols {
				c, f, b := tv.state.Cell(x, y)
				if f != fg || b != cellBG {
					break
				}
				if c == 0 {
					c = ' '
				}
				chars = append(chars, c)
				x++
			}
			fgColor := colorFor(fg, false)
			bgColor := colorFor(cellBG, true)
			if bgColor != walk.RGB(12, 14, 16) {
				brush, e := tv.brush(bgColor)
				if e != nil {
					return e
				}
				if e = canvas.FillRectangle(brush, walk.Rectangle{X: start * cellWidth, Y: y * cellHeight, Width: (x - start) * cellWidth, Height: cellHeight}); e != nil {
					return e
				}
			}
			if ch != 0 && strings.TrimSpace(string(chars)) != "" {
				e := canvas.DrawText(string(chars), tv.font, fgColor, walk.Rectangle{X: start * cellWidth, Y: y * cellHeight, Width: (x-start)*cellWidth + cellWidth, Height: cellHeight + 2}, walk.TextLeft|walk.TextTop|walk.TextSingleLine|walk.TextNoPrefix|walk.TextNoClip)
				if e != nil {
					return e
				}
			}
		}
	}
	if tv.state.CursorVisible() && tv.Focused() {
		x, y := tv.state.Cursor()
		brush, e := tv.brush(walk.RGB(230, 233, 236))
		if e != nil {
			return e
		}
		return canvas.FillRectangle(brush, walk.Rectangle{X: x * cellWidth, Y: y*cellHeight + cellHeight - 2, Width: cellWidth, Height: 2})
	}
	return nil
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
		tv.Invalidate()
	case win.WM_RBUTTONUP:
		tv.paste()
		return 0
	case win.WM_SETFOCUS, win.WM_KILLFOCUS:
		tv.Invalidate()
	case win.WM_SIZE:
		tv.resizeTerminal()
	case win.WM_CHAR:
		tv.sendUTF16Unit(uint16(wParam))
		return 0
	case win.WM_KEYDOWN:
		if (wParam == win.VK_INSERT && walk.ShiftDown()) || (wParam == uintptr(walk.KeyV) && walk.ControlDown() && walk.ShiftDown()) {
			tv.paste()
			return 0
		}
		if tv.sendSpecial(wParam) {
			return 0
		}
	}
	return tv.CustomWidget.WndProc(hwnd, msg, wParam, lParam)
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
