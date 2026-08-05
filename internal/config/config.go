// +build windows

package config

import (
	"strconv"
	"syscall"
	"unsafe"
)

const path = `Software\UniCom`

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	regCreateKeyEx  = advapi32.NewProc("RegCreateKeyExW")
	regOpenKeyEx    = advapi32.NewProc("RegOpenKeyExW")
	regQueryValueEx = advapi32.NewProc("RegQueryValueExW")
	regSetValueEx   = advapi32.NewProc("RegSetValueExW")
	regCloseKey     = advapi32.NewProc("RegCloseKey")
)

const hkcu = uintptr(0x80000001)

func Load(name, fallback string) string {
	var h uintptr
	p, _ := syscall.UTF16PtrFromString(path)
	r, _, _ := regOpenKeyEx.Call(hkcu, uintptr(unsafe.Pointer(p)), 0, 0x20019, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return fallback
	}
	defer regCloseKey.Call(h)
	n, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	r, _, _ = regQueryValueEx.Call(h, uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&size)))
	if r != 0 || size < 2 {
		return fallback
	}
	b := make([]uint16, size/2+1)
	r, _, _ = regQueryValueEx.Call(h, uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return fallback
	}
	return syscall.UTF16ToString(b)
}

func Save(name, value string) {
	var h uintptr
	var disposition uint32
	p, _ := syscall.UTF16PtrFromString(path)
	r, _, _ := regCreateKeyEx.Call(hkcu, uintptr(unsafe.Pointer(p)), 0, 0, 0, 0x2001F, 0, uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&disposition)))
	if r != 0 {
		return
	}
	defer regCloseKey.Call(h)
	n, _ := syscall.UTF16PtrFromString(name)
	v, _ := syscall.UTF16FromString(value)
	regSetValueEx.Call(h, uintptr(unsafe.Pointer(n)), 0, 1, uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))
}

func LoadInt(name string, fallback int) int {
	v, err := strconv.Atoi(Load(name, ""))
	if err != nil {
		return fallback
	}
	return v
}
func SaveInt(name string, value int) { Save(name, strconv.Itoa(value)) }
