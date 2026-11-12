//go:build windows
// +build windows

package terminalview

import (
	"bytes"
	"fmt"
	"syscall"
	"testing"
	"unsafe"

	"github.com/lxn/win"
)

// Render into a real GDI bitmap. Moving the selection boundary must not move
// a single glyph pixel, including fallback CJK fonts and surrogate pairs.
func TestGridGlyphPositionsIndependentOfRunBoundaries(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		t.Run(fmt.Sprint(dpi), func(t *testing.T) {
			hdc := win.CreateCompatibleDC(0)
			if hdc == 0 {
				t.Fatal("CreateCompatibleDC failed")
			}
			defer win.DeleteDC(hdc)
			var font gridFont
			if err := font.ensure(hdc, dpi); err != nil {
				t.Fatal(err)
			}
			defer font.dispose()
			oldFont := win.SelectObject(hdc, win.HGDIOBJ(font.handle))
			defer win.SelectObject(hdc, oldFont)
			glyphs := []rune("Abc012中英文XYZ😀END")
			widths := []int{1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 2, 1, 1, 1}
			cols := 0
			for _, w := range widths {
				cols += w
			}
			width, height := cols*font.width, font.height
			info := win.BITMAPINFOHEADER{BiSize: uint32(unsafe.Sizeof(win.BITMAPINFOHEADER{})),
				BiWidth: int32(width), BiHeight: -int32(height), BiPlanes: 1, BiBitCount: 32}
			var bits unsafe.Pointer
			bitmap := win.CreateDIBSection(hdc, &info, win.DIB_RGB_COLORS, &bits, 0, 0)
			if bitmap == 0 || bits == nil {
				t.Fatal("CreateDIBSection failed")
			}
			defer win.DeleteObject(win.HGDIOBJ(bitmap))
			oldBitmap := win.SelectObject(hdc, win.HGDIOBJ(bitmap))
			defer win.SelectObject(hdc, oldBitmap)
			win.SetBkMode(hdc, win.TRANSPARENT)
			win.SetTextColor(hdc, 0xffffff)
			pixels := (*[1 << 24]byte)(bits)[:width*height*4]
			flush := syscall.NewLazyDLL("gdi32.dll").NewProc("GdiFlush")
			render := func(split int) []byte {
				for i := range pixels {
					pixels[i] = 0
				}
				x, start := 0, 0
				for _, end := range []int{split, len(glyphs)} {
					var text []uint16
					var advances []int32
					runWidth := 0
					for i := start; i < end; i++ {
						w := widths[i] * font.width
						text, advances = appendGlyph(text, advances, glyphs[i], w)
						runWidth += w
					}
					if err := drawGridRun(hdc, x, 0, runWidth, height, text, advances); err != nil {
						t.Fatal(err)
					}
					x += runWidth
					start = end
				}
				flush.Call()
				return append([]byte(nil), pixels...)
			}
			baseline := render(len(glyphs))
			if bytes.Equal(baseline, make([]byte, len(baseline))) {
				t.Fatal("blank rendering")
			}
			for split := 1; split < len(glyphs); split++ {
				if got := render(split); !bytes.Equal(got, baseline) {
					t.Fatalf("glyph pixels moved when selection boundary split before glyph %d (%q)", split, glyphs[split])
				}
			}
		})
	}
}
