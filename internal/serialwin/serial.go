// +build windows

package serialwin

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

type Config struct {
	Name     string
	BaudRate uint32
	DataBits byte
	Parity   byte
	StopBits byte
	Flow     byte
	DTR      bool
	RTS      bool
}

const (
	ParityNone byte = iota
	ParityOdd
	ParityEven
	ParityMark
	ParitySpace
)

const (
	StopOne byte = iota
	StopOneAndHalf
	StopTwo
)

const (
	FlowNone byte = iota
	FlowRTSCTS
	FlowXONXOFF
)

const (
	setRTS   = 3
	clearRTS = 4
	setDTR   = 5
	clearDTR = 6
)

type dcb struct {
	Length, BaudRate, Flags                        uint32
	Reserved, XonLim, XoffLim                      uint16
	ByteSize, Parity, StopBits                     byte
	XonChar, XoffChar, ErrorChar, EofChar, EvtChar byte
	Reserved1                                      uint16
}

type commTimeouts struct {
	ReadIntervalTimeout, ReadTotalTimeoutMultiplier, ReadTotalTimeoutConstant uint32
	WriteTotalTimeoutMultiplier, WriteTotalTimeoutConstant                    uint32
}

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	getCommState    = kernel32.NewProc("GetCommState")
	setCommState    = kernel32.NewProc("SetCommState")
	setCommTimeouts = kernel32.NewProc("SetCommTimeouts")
	setupComm       = kernel32.NewProc("SetupComm")
	purgeComm       = kernel32.NewProc("PurgeComm")
	queryDosDevice  = kernel32.NewProc("QueryDosDeviceW")
	escapeComm      = kernel32.NewProc("EscapeCommFunction")
)

type Port struct {
	mu     sync.Mutex
	h      syscall.Handle
	closed bool
}

func Open(c Config) (*Port, error) {
	if c.Name == "" || c.BaudRate == 0 {
		return nil, errors.New("串口和波特率不能为空")
	}
	name := `\\.\` + strings.ToUpper(c.Name)
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 %s 失败: %v", c.Name, err)
	}
	port := &Port{h: h}
	if err = port.configure(c); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	return port, nil
}

func (p *Port) configure(c Config) error {
	setupComm.Call(uintptr(p.h), 64*1024, 64*1024)
	var d dcb
	d.Length = uint32(unsafe.Sizeof(d))
	r, _, e := getCommState.Call(uintptr(p.h), uintptr(unsafe.Pointer(&d)))
	if r == 0 {
		return fmt.Errorf("读取串口参数失败: %v", e)
	}
	d.BaudRate, d.ByteSize, d.Parity, d.StopBits = c.BaudRate, c.DataBits, c.Parity, c.StopBits
	// Keep fBinary and clear parity, flow-control, DTR, and RTS fields.
	d.Flags = (d.Flags &^ uint32(0x7FFE)) | 1
	if c.Parity != ParityNone {
		d.Flags |= 2
	}
	if c.DTR {
		d.Flags |= 1 << 4
	}
	if c.RTS {
		d.Flags |= 1 << 12
	}
	if c.Flow == FlowRTSCTS {
		d.Flags |= (1 << 2) | (2 << 12)
	}
	if c.Flow == FlowXONXOFF {
		d.Flags |= (1 << 8) | (1 << 9)
	}
	d.XonChar, d.XoffChar = 0x11, 0x13
	r, _, e = setCommState.Call(uintptr(p.h), uintptr(unsafe.Pointer(&d)))
	if r == 0 {
		return fmt.Errorf("设置串口参数失败: %v", e)
	}
	t := commTimeouts{ReadIntervalTimeout: 50, ReadTotalTimeoutConstant: 100, WriteTotalTimeoutConstant: 1000}
	r, _, e = setCommTimeouts.Call(uintptr(p.h), uintptr(unsafe.Pointer(&t)))
	if r == 0 {
		return fmt.Errorf("设置串口超时失败: %v", e)
	}
	purgeComm.Call(uintptr(p.h), 0x0004|0x0008)
	return nil
}

func (p *Port) Read(buf []byte) (int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, errors.New("串口已关闭")
	}
	h := p.h
	p.mu.Unlock()
	var n uint32
	err := syscall.ReadFile(h, buf, &n, nil)
	return int(n), err
}

func (p *Port) Write(buf []byte) (int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, errors.New("串口已关闭")
	}
	h := p.h
	p.mu.Unlock()
	var n uint32
	err := syscall.WriteFile(h, buf, &n, nil)
	return int(n), err
}

func (p *Port) setLineState(command uintptr) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("串口已关闭")
	}
	h := p.h
	p.mu.Unlock()
	r, _, e := escapeComm.Call(uintptr(h), command)
	if r == 0 {
		return fmt.Errorf("设置串口控制线失败: %v", e)
	}
	return nil
}

func (p *Port) SetDTR(enabled bool) error {
	if enabled {
		return p.setLineState(setDTR)
	}
	return p.setLineState(clearDTR)
}

func (p *Port) SetRTS(enabled bool) error {
	if enabled {
		return p.setLineState(setRTS)
	}
	return p.setLineState(clearRTS)
}

func (p *Port) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	h := p.h
	p.mu.Unlock()

	// Purge pending I/O before releasing the handle. Close is idempotent, so a
	// removal notification and a read failure can safely race.
	purgeComm.Call(uintptr(h), 0x0001|0x0002|0x0004|0x0008)
	return syscall.CloseHandle(h)
}

func ListPorts() []string {
	ports := make([]string, 0)
	buf := make([]uint16, 512)
	for i := 1; i <= 256; i++ {
		name := "COM" + strconv.Itoa(i)
		p, _ := syscall.UTF16PtrFromString(name)
		r, _, _ := queryDosDevice.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if r != 0 {
			ports = append(ports, name)
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, _ := strconv.Atoi(ports[i][3:])
		b, _ := strconv.Atoi(ports[j][3:])
		return a < b
	})
	return ports
}

func Exists(name string) bool {
	buf := make([]uint16, 512)
	p, err := syscall.UTF16PtrFromString(strings.ToUpper(strings.TrimSpace(name)))
	if err != nil {
		return false
	}
	r, _, _ := queryDosDevice.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return r != 0
}
