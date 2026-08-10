// +build windows

package devicewatch

import (
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

const (
	deviceWatcherClass       = "UniCom Device Watcher"
	deviceNotifyWindowHandle = 0
	deviceTypeInterface      = 5
	deviceArrival            = 0x8000
	deviceRemoveComplete     = 0x8004
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type deviceBroadcastInterface struct {
	Size       uint32
	DeviceType uint32
	Reserved   uint32
	ClassGUID  guid
	Name       uint16
}

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	registerDeviceNotification   = user32.NewProc("RegisterDeviceNotificationW")
	unregisterDeviceNotification = user32.NewProc("UnregisterDeviceNotification")
	comPortInterfaceGUID         = guid{
		Data1: 0x86E0D1E0,
		Data2: 0x8089,
		Data3: 0x11D0,
		Data4: [8]byte{0x9C, 0xE4, 0x08, 0x00, 0x3E, 0x30, 0x1F, 0x73},
	}
)

func init() {
	walk.MustRegisterWindowClass(deviceWatcherClass)
}

// Watcher receives COM-port interface arrival and removal notifications. It
// is a hidden child window and consumes no space in the parent layout.
type Watcher struct {
	walk.WidgetBase
	notification uintptr
	onChange     func()
}

func New(parent walk.Container, onChange func()) (*Watcher, error) {
	w := &Watcher{onChange: onChange}
	if err := walk.InitWidget(w, parent, deviceWatcherClass, 0, 0); err != nil {
		return nil, err
	}

	filter := deviceBroadcastInterface{
		Size:       uint32(unsafe.Sizeof(deviceBroadcastInterface{})),
		DeviceType: deviceTypeInterface,
		ClassGUID:  comPortInterfaceGUID,
	}
	h, _, err := registerDeviceNotification.Call(
		uintptr(w.Handle()),
		uintptr(unsafe.Pointer(&filter)),
		deviceNotifyWindowHandle,
	)
	if h == 0 {
		w.Dispose()
		return nil, err
	}
	w.notification = h
	w.Disposing().Attach(func() {
		if w.notification != 0 {
			unregisterDeviceNotification.Call(w.notification)
			w.notification = 0
		}
	})
	return w, nil
}

func (w *Watcher) MinSizeHint() walk.Size {
	return walk.Size{}
}

func (w *Watcher) SizeHint() walk.Size {
	return walk.Size{}
}

func (w *Watcher) WndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	if msg == win.WM_DEVICECHANGE && (wParam == deviceArrival || wParam == deviceRemoveComplete) {
		if w.onChange != nil {
			w.onChange()
		}
	}
	return w.WidgetBase.WndProc(hwnd, msg, wParam, lParam)
}
