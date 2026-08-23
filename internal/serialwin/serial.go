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
	"unicode/utf8"
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

type comStat struct {
	flags    uint32
	cbInQue  uint32
	cbOutQue uint32
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
	clearCommError  = kernel32.NewProc("ClearCommError")
)

var DefaultBaudCandidates = []uint32{
	115200,
	9600,
	57600,
	38400,
	19200,
	230400,
	460800,
	921600,
	1500000,
	2000000,
	4800,
	2400,
	1200,
	76800,
	128000,
	256000,
	500000,
	1000000,
	3000000,
}

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

func (p *Port) SetBaudRate(baud uint32) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("串口已关闭")
	}
	h := p.h
	p.mu.Unlock()

	var d dcb
	d.Length = uint32(unsafe.Sizeof(d))
	r, _, e := getCommState.Call(uintptr(h), uintptr(unsafe.Pointer(&d)))
	if r == 0 {
		return fmt.Errorf("读取串口参数失败: %v", e)
	}
	d.BaudRate = baud
	r, _, e = setCommState.Call(uintptr(h), uintptr(unsafe.Pointer(&d)))
	if r == 0 {
		return fmt.Errorf("设置串口波特率失败: %v", e)
	}
	purgeComm.Call(uintptr(h), 0x0004|0x0008)
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

func (p *Port) sample(timeoutMs uint32) ([]byte, uint32, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, 0, errors.New("串口已关闭")
	}
	h := p.h
	p.mu.Unlock()

	t := commTimeouts{
		ReadIntervalTimeout:        20,
		ReadTotalTimeoutMultiplier: 0,
		ReadTotalTimeoutConstant:   timeoutMs,
		WriteTotalTimeoutConstant:  1000,
	}
	setCommTimeouts.Call(uintptr(h), uintptr(unsafe.Pointer(&t)))
	purgeComm.Call(uintptr(h), 0x0004|0x0008)

	buf := make([]byte, 512)
	var n uint32
	_ = syscall.ReadFile(h, buf, &n, nil)

	var commErrors uint32
	var stat comStat
	clearCommError.Call(uintptr(h), uintptr(unsafe.Pointer(&commErrors)), uintptr(unsafe.Pointer(&stat)))

	return buf[:n], commErrors, nil
}

func ScoreSample(data []byte, commErrors uint32) float64 {
	if len(data) == 0 {
		return 0.0
	}
	// Severe penalty for framing error (0x0008)
	if commErrors&0x0008 != 0 {
		return 0.01
	}
	if commErrors&0x0004 != 0 {
		return 0.05
	}

	printable := 0
	suspicious := 0
	for _, b := range data {
		if (b >= 0x20 && b <= 0x7E) || b == '\r' || b == '\n' || b == '\t' {
			printable++
		} else if b == 0x00 || b == 0xFF || (b < 0x20 && b != '\r' && b != '\n' && b != '\t') {
			suspicious++
		}
	}
	ratio := float64(printable) / float64(len(data))
	if utf8.Valid(data) && ratio > 0.5 {
		ratio += 0.3
	}
	if commErrors == 0 && ratio < 0.5 {
		return 0.6 - (float64(suspicious) / float64(len(data)) * 0.2)
	}
	return ratio
}

func DetectBaudRate(name string, candidates []uint32, onProgress func(baud uint32)) (uint32, error) {
	if name == "" {
		return 0, errors.New("请先选择或输入串口端口号")
	}
	if len(candidates) == 0 {
		candidates = DefaultBaudCandidates
	}

	c := Config{
		Name:     name,
		BaudRate: candidates[0],
		DataBits: 8,
		Parity:   ParityNone,
		StopBits: StopOne,
		Flow:     FlowNone,
	}
	p, err := Open(c)
	if err != nil {
		return 0, err
	}
	defer p.Close()

	var bestBaud uint32
	var bestScore float64
	totalBytes := 0

	for _, baud := range candidates {
		if onProgress != nil {
			onProgress(baud)
		}
		if err := p.SetBaudRate(baud); err != nil {
			continue
		}
		data, commErrors, err := p.sample(80)
		if err != nil {
			continue
		}
		if len(data) > 0 {
			totalBytes += len(data)
			score := ScoreSample(data, commErrors)
			if score > bestScore {
				bestScore = score
				bestBaud = baud
			}
			if score >= 0.95 && len(data) >= 8 && commErrors == 0 {
				return baud, nil
			}
		}
	}

	if totalBytes == 0 {
		return 0, errors.New("未检测到任何串口数据，请确保下位机已连接并正在发送数据")
	}
	if bestScore > 0 && bestBaud > 0 {
		return bestBaud, nil
	}
	return 0, errors.New("无法确定有效波特率，收到的数据均为乱码或错误帧")
}

