// +build windows

package wrapedit

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

const (
	richEditClass     = "RichEdit20W"
	emExGetSel        = 0x0434
	emExLimitText     = 0x0435
	emExSetSel        = 0x0437
	emSetTargetDevice = 0x0448
)

var (
	loadOnce    sync.Once
	richEditDLL *syscall.DLL
	loadErr     error
)

type charRange struct {
	Min int32
	Max int32
}

type Edit struct {
	walk.WidgetBase
	readOnlyChangedPublisher walk.EventPublisher
	textChangedPublisher     walk.EventPublisher
}

func ensureRichEdit() error {
	loadOnce.Do(func() {
		richEditDLL, loadErr = syscall.LoadDLL("riched20.dll")
	})
	return loadErr
}

func New(parent walk.Container, style uint32) (*Edit, error) {
	if err := ensureRichEdit(); err != nil {
		return nil, err
	}
	e := new(Edit)
	if err := walk.InitWidget(
		e,
		parent,
		richEditClass,
		win.WS_TABSTOP|win.WS_VISIBLE|win.ES_MULTILINE|win.ES_WANTRETURN|style,
		win.WS_EX_CLIENTEDGE,
	); err != nil {
		return nil, err
	}

	e.GraphicsEffects().Add(walk.InteractionEffect)
	e.GraphicsEffects().Add(walk.FocusEffect)
	e.MustRegisterProperty("ReadOnly", walk.NewProperty(
		func() interface{} { return e.ReadOnly() },
		func(v interface{}) error { return e.SetReadOnly(v.(bool)) },
		e.readOnlyChangedPublisher.Event(),
	))
	e.MustRegisterProperty("Text", walk.NewProperty(
		func() interface{} { return e.Text() },
		func(v interface{}) error { return e.SetText(v.(string)) },
		e.textChangedPublisher.Event(),
	))
	return e, nil
}

func (*Edit) LayoutFlags() walk.LayoutFlags {
	return walk.ShrinkableHorz | walk.ShrinkableVert | walk.GrowableHorz | walk.GrowableVert | walk.GreedyHorz | walk.GreedyVert
}

func (e *Edit) MinSizeHint() walk.Size {
	return walk.Size{20, 12}
}

func (e *Edit) SizeHint() walk.Size {
	return walk.Size{100, 100}
}

func (e *Edit) TextLength() int {
	return int(e.SendMessage(win.WM_GETTEXTLENGTH, 0, 0))
}

func (e *Edit) Text() string {
	buf := make([]uint16, e.TextLength()+1)
	e.SendMessage(win.WM_GETTEXT, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	return syscall.UTF16ToString(buf)
}

func (e *Edit) SetText(value string) error {
	if value == e.Text() {
		return nil
	}
	p, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	if e.SendMessage(win.WM_SETTEXT, 0, uintptr(unsafe.Pointer(p))) == 0 {
		return errors.New("setting text failed")
	}
	e.textChangedPublisher.Publish()
	return nil
}

func (e *Edit) TextSelection() (start, end int) {
	r := charRange{}
	e.SendMessage(emExGetSel, 0, uintptr(unsafe.Pointer(&r)))
	return int(r.Min), int(r.Max)
}

func (e *Edit) SetTextSelection(start, end int) {
	r := charRange{Min: int32(start), Max: int32(end)}
	e.SendMessage(emExSetSel, 0, uintptr(unsafe.Pointer(&r)))
}

func (e *Edit) ReplaceSelectedText(text string, canUndo bool) {
	p, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	e.SendMessage(win.EM_REPLACESEL, uintptr(win.BoolToBOOL(canUndo)), uintptr(unsafe.Pointer(p)))
}

func (e *Edit) AppendText(value string) {
	start, end := e.TextSelection()
	length := e.TextLength()
	e.SetTextSelection(length, length)
	e.ReplaceSelectedText(value, false)
	e.SetTextSelection(start, end)
}

func (e *Edit) ReadOnly() bool {
	return uint32(win.GetWindowLong(e.Handle(), win.GWL_STYLE))&win.ES_READONLY != 0
}

func (e *Edit) SetReadOnly(readOnly bool) error {
	if e.SendMessage(win.EM_SETREADONLY, uintptr(win.BoolToBOOL(readOnly)), 0) == 0 {
		return errors.New("setting read-only state failed")
	}
	e.readOnlyChangedPublisher.Publish()
	return nil
}

func (e *Edit) SetMaxLength(value int) {
	e.SendMessage(emExLimitText, 0, uintptr(value))
}

func (e *Edit) TextChanged() *walk.Event {
	return e.textChangedPublisher.Event()
}

func (e *Edit) SetWordWrap(wrap bool) {
	style := win.GetWindowLong(e.Handle(), win.GWL_STYLE)
	target := style
	if wrap {
		target &^= int32(win.WS_HSCROLL | win.ES_AUTOHSCROLL)
	} else {
		target |= int32(win.WS_HSCROLL | win.ES_AUTOHSCROLL)
	}
	if style != target {
		win.SetWindowLong(e.Handle(), win.GWL_STYLE, target)
		win.SetWindowPos(e.Handle(), 0, 0, 0, 0, 0, win.SWP_FRAMECHANGED|win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE)
	}
	lineWidth := uintptr(1)
	if wrap {
		lineWidth = 0
	}
	e.SendMessage(emSetTargetDevice, 0, lineWidth)
	e.Invalidate()
}

func (e *Edit) WndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_COMMAND:
		if win.HIWORD(uint32(wParam)) == win.EN_CHANGE {
			e.textChangedPublisher.Publish()
		}
	case win.WM_GETDLGCODE:
		if wParam == win.VK_RETURN {
			return win.DLGC_WANTALLKEYS
		}
		return win.DLGC_HASSETSEL | win.DLGC_WANTARROWS | win.DLGC_WANTCHARS
	case win.WM_KEYDOWN:
		if walk.Key(wParam) == walk.KeyA && walk.ControlDown() {
			e.SetTextSelection(0, -1)
		}
	}
	return e.WidgetBase.WndProc(hwnd, msg, wParam, lParam)
}

type View struct {
	AssignTo      **Edit
	Font          declarative.Font
	MinSize       declarative.Size
	MaxSize       declarative.Size
	StretchFactor int
	ReadOnly      declarative.Property
	Text          declarative.Property
	Visible       declarative.Property
	Enabled       declarative.Property
	VScroll       bool
	HScroll       bool
	MaxLength     int
	OnTextChanged walk.EventHandler
}

func (v View) Create(builder *declarative.Builder) error {
	var style uint32
	if v.HScroll {
		style |= win.WS_HSCROLL | win.ES_AUTOHSCROLL
	}
	if v.VScroll {
		style |= win.WS_VSCROLL | win.ES_AUTOVSCROLL
	}
	e, err := New(builder.Parent(), style)
	if err != nil {
		return err
	}
	if v.AssignTo != nil {
		*v.AssignTo = e
	}
	return builder.InitWidget(v, e, func() error {
		if v.MaxLength > 0 {
			e.SetMaxLength(v.MaxLength)
		}
		if v.OnTextChanged != nil {
			e.TextChanged().Attach(v.OnTextChanged)
		}
		return nil
	})
}
