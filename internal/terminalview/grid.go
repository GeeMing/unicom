//go:build windows
// +build windows

package terminalview

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/hinshun/vt10x"
	"github.com/lxn/win"
)

var extTextOut = syscall.NewLazyDLL("gdi32.dll").NewProc("ExtTextOutW")
var getCapture = syscall.NewLazyDLL("user32.dll").NewProc("GetCapture")

type terminalCell struct {
	ch     rune
	fg, bg vt10x.Color
}

// A selection owns its screen and scrollback, so even cursor-addressed output
// and history eviction cannot change either the highlighted text or the copy.
type screenSnapshot struct {
	rows, cols, history int
	cells               []terminalCell
}

func (tv *TerminalView) captureSelection() {
	if tv.snapshot != nil || tv.state == nil {
		return
	}
	tv.state.Lock()
	defer tv.state.Unlock()
	rows, cols := tv.state.Size()
	s := &screenSnapshot{rows: rows, cols: cols, history: tv.state.ScrollbackSize()}
	s.cells = make([]terminalCell, (s.history+rows)*cols)
	for y := 0; y < s.history+rows; y++ {
		for x := 0; x < cols; x++ {
			c := &s.cells[y*cols+x]
			if y < s.history {
				c.ch, c.fg, c.bg = tv.state.ScrollbackCell(x, y)
			} else {
				c.ch, c.fg, c.bg = tv.state.Cell(x, y-s.history)
			}
		}
	}
	tv.snapshot = s
}

// Caller holds the live state lock when no snapshot is active.
func (tv *TerminalView) viewCell(x, y int) terminalCell {
	if s := tv.snapshot; s != nil {
		return s.cells[y*s.cols+x]
	}
	var c terminalCell
	history := tv.state.ScrollbackSize()
	if y < history {
		c.ch, c.fg, c.bg = tv.state.ScrollbackCell(x, y)
	} else {
		c.ch, c.fg, c.bg = tv.state.Cell(x, y-history)
	}
	return c
}

func (tv *TerminalView) viewSize() (rows, cols, history int) {
	if s := tv.snapshot; s != nil {
		return s.rows, s.cols, s.history
	}
	if tv.state == nil {
		return 1, 1, 0
	}
	tv.state.Lock()
	defer tv.state.Unlock()
	rows, cols = tv.state.Size()
	return rows, cols, tv.state.ScrollbackSize()
}

type gridFont struct {
	handle             win.HFONT
	dpi, width, height int
}

func (f *gridFont) ensure(hdc win.HDC, dpi int) error {
	if f.handle != 0 && f.dpi == dpi {
		return nil
	}
	lf := win.LOGFONT{LfHeight: -int32((11*dpi + 36) / 72), LfCharSet: win.DEFAULT_CHARSET,
		LfPitchAndFamily: win.FIXED_PITCH | win.FF_MODERN}
	copy(lf.LfFaceName[:], syscall.StringToUTF16("Consolas"))
	handle := win.CreateFontIndirect(&lf)
	if handle == 0 {
		return fmt.Errorf("create terminal font failed")
	}
	old := win.SelectObject(hdc, win.HGDIOBJ(handle))
	var metrics win.TEXTMETRIC
	ok := win.GetTextMetrics(hdc, &metrics)
	win.SelectObject(hdc, old)
	if !ok || metrics.TmAveCharWidth <= 0 || metrics.TmHeight <= 0 {
		win.DeleteObject(win.HGDIOBJ(handle))
		return fmt.Errorf("measure terminal font failed")
	}
	f.dispose()
	f.handle, f.dpi = handle, dpi
	f.width, f.height = int(metrics.TmAveCharWidth), int(metrics.TmHeight+metrics.TmExternalLeading)
	return nil
}

func (f *gridFont) dispose() {
	if f.handle != 0 {
		win.DeleteObject(win.HGDIOBJ(f.handle))
		f.handle = 0
	}
}

// UTF-16 surrogate pairs share one advance. Wide-cell continuation markers
// never become extra spaces: the lead glyph already advances by two cells.
func appendGlyph(text []uint16, advances []int32, ch rune, width int) ([]uint16, []int32) {
	if ch > 0xffff {
		hi, lo := utf16.EncodeRune(ch)
		return append(text, uint16(hi), uint16(lo)), append(advances, 0, int32(width))
	}
	return append(text, uint16(ch)), append(advances, int32(width))
}

func drawGridRun(hdc win.HDC, x, y, width, height int, text []uint16, advances []int32) error {
	// Clip and shape each glyph independently of colour-run boundaries. GDI
	// can overhang even a monospace glyph into its neighbour; clipping only a
	// colour run would make those pixels appear/disappear during selection.
	right := x + width
	for i := 0; i < len(text); {
		n, advance := 1, int(advances[i])
		if text[i] >= 0xd800 && text[i] <= 0xdbff && i+1 < len(text) {
			n, advance = 2, advance+int(advances[i+1])
		}
		if text[i] != ' ' {
			r := win.RECT{Left: int32(x), Top: int32(y), Right: int32(clampInt(x+advance, x, right)), Bottom: int32(y + height)}
			const etoClipped = 0x0004
			ok, _, _ := extTextOut.Call(uintptr(hdc), uintptr(x), uintptr(y), etoClipped,
				uintptr(unsafe.Pointer(&r)), uintptr(unsafe.Pointer(&text[i])), uintptr(n), uintptr(unsafe.Pointer(&advances[i])))
			if ok == 0 {
				return fmt.Errorf("draw terminal text failed")
			}
		}
		x += advance
		i += n
	}
	return nil
}
