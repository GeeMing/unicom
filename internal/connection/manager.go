// +build windows

package connection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"unicom/internal/serialwin"
)

type State int

const (
	StateClosed State = iota
	StateConnecting
	StateConnected
	StateWaiting
)

type Event struct {
	State   State
	Message string
}

type Manager struct {
	mu         sync.Mutex
	writeMu    sync.Mutex
	connection io.ReadWriteCloser
	serialPort *serialwin.Port
	config     serialwin.Config
	tcpAddress string
	mode       string
	wanted     bool
	present    bool
	generation uint64
	cancel     context.CancelFunc
	wake       chan struct{}
	wg         sync.WaitGroup
	onData     func([]byte)
	onEvent    func(Event)
	onTX       func(int)
}

func New(onData func([]byte), onEvent func(Event), onTX func(int)) *Manager {
	return &Manager{
		onData:  onData,
		onEvent: onEvent,
		onTX:    onTX,
		wake:    make(chan struct{}, 1),
	}
}

func (m *Manager) Open(c serialwin.Config) {
	m.stopCurrent()
	m.drainSignal()

	m.mu.Lock()
	m.config = c
	m.mode = "serial"
	m.tcpAddress = ""
	m.wanted = true
	m.present = serialwin.Exists(c.Name)
	m.generation++
	gen := m.generation
	m.wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	go m.connectLoop(ctx, gen)
}

func (m *Manager) OpenTCP(address string) {
	m.openNetwork("tcp", address)
}

func (m *Manager) OpenUDP(address string) {
	m.openNetwork("udp", address)
}

func (m *Manager) openNetwork(mode, address string) {
	m.stopCurrent()
	m.drainSignal()

	m.mu.Lock()
	m.mode = mode
	m.tcpAddress = address
	m.wanted = true
	m.present = false
	m.generation++
	gen := m.generation
	m.wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	go m.connectLoop(ctx, gen)
}

func (m *Manager) stopCurrent() {
	m.mu.Lock()
	m.wanted = false
	m.generation++
	p := m.connection
	cancel := m.cancel
	m.cancel = nil
	m.connection = nil
	m.serialPort = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if p != nil {
		_ = p.Close()
	}
	m.signal()
	m.wg.Wait()
}

func (m *Manager) connectLoop(ctx context.Context, gen uint64) {
	defer m.wg.Done()

	for {
		m.mu.Lock()
		if !m.wanted || gen != m.generation {
			m.mu.Unlock()
			return
		}
		c := m.config
		mode := m.mode
		tcpAddress := m.tcpAddress
		m.mu.Unlock()

		if mode == "tcp" || mode == "udp" {
			if !m.connectNetwork(ctx, gen, mode, tcpAddress) {
				return
			}
			continue
		}

		if !serialwin.Exists(c.Name) {
			m.setPresent(false)
			m.emit(Event{StateWaiting, fmt.Sprintf("等待设备事件: %s", c.Name)})
			if !m.waitForDevice(gen) {
				return
			}
			continue
		}

		m.setPresent(true)
		m.emit(Event{StateConnecting, "正在打开 " + c.Name + "..."})
		p, err := serialwin.Open(c)
		if err != nil {
			m.emit(Event{StateWaiting, fmt.Sprintf("%s 暂不可用，等待设备事件", c.Name)})
			if !m.waitForDevice(gen) {
				return
			}
			continue
		}

		m.mu.Lock()
		if !m.wanted || gen != m.generation {
			m.mu.Unlock()
			_ = p.Close()
			return
		}
		m.connection = p
		m.serialPort = p
		m.mu.Unlock()
		m.emit(Event{StateConnected, c.Name + " 已连接"})

		err = m.readLoop(gen, p)
		m.mu.Lock()
		if m.connection == p {
			m.connection = nil
			m.serialPort = nil
		}
		wanted := m.wanted && gen == m.generation
		m.mu.Unlock()
		_ = p.Close()
		if !wanted {
			return
		}

		m.emit(Event{StateWaiting, fmt.Sprintf("连接中断: %v；等待设备事件", err)})
		if !m.waitForDevice(gen) {
			return
		}
	}
}

func (m *Manager) connectNetwork(ctx context.Context, gen uint64, mode, address string) bool {
	protocol := strings.ToUpper(mode)
	m.emit(Event{StateConnecting, protocol + " 正在连接 " + address + "..."})
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	c, err := dialer.DialContext(ctx, mode, address)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		m.emit(Event{StateWaiting, fmt.Sprintf("%s 连接失败: %v；2 秒后重试", protocol, err)})
		return m.waitRetry(gen, 2*time.Second)
	}

	m.mu.Lock()
	if !m.wanted || gen != m.generation {
		m.mu.Unlock()
		_ = c.Close()
		return false
	}
	m.connection = c
	m.serialPort = nil
	m.mu.Unlock()
	m.emit(Event{StateConnected, protocol + " " + address + " 已连接"})

	err = m.readLoop(gen, c)
	m.mu.Lock()
	if m.connection == c {
		m.connection = nil
	}
	wanted := m.wanted && gen == m.generation
	m.mu.Unlock()
	_ = c.Close()
	if !wanted {
		return false
	}
	m.emit(Event{StateWaiting, fmt.Sprintf("%s 连接中断: %v；2 秒后重试", protocol, err)})
	return m.waitRetry(gen, 2*time.Second)
}

func (m *Manager) readLoop(gen uint64, p io.ReadWriteCloser) error {
	buf := make([]byte, 8192)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			m.onData(data)
		}
		if err != nil {
			return err
		}

		m.mu.Lock()
		active := m.wanted && gen == m.generation && m.connection == p
		mode := m.mode
		m.mu.Unlock()
		if !active {
			if mode == "tcp" || mode == "udp" {
				return fmt.Errorf("%s 连接已关闭", strings.ToUpper(mode))
			}
			return errors.New("串口已关闭")
		}
	}
}

func (m *Manager) waitRetry(gen uint64, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-m.wake:
	}
	m.mu.Lock()
	active := m.wanted && gen == m.generation
	m.mu.Unlock()
	return active
}

func (m *Manager) waitForDevice(gen uint64) bool {
	m.mu.Lock()
	active := m.wanted && gen == m.generation
	wake := m.wake
	m.mu.Unlock()
	if !active {
		return false
	}

	<-wake
	m.mu.Lock()
	active = m.wanted && gen == m.generation
	m.mu.Unlock()
	return active
}

// DeviceChanged handles a WM_DEVICECHANGE notification. Events for unrelated
// COM ports are ignored by comparing the target port's current presence.
func (m *Manager) DeviceChanged() {
	m.mu.Lock()
	if !m.wanted {
		m.mu.Unlock()
		return
	}
	c := m.config
	mode := m.mode
	wasPresent := m.present
	m.mu.Unlock()

	if mode != "serial" {
		return
	}
	present := serialwin.Exists(c.Name)
	if present == wasPresent {
		return
	}

	m.mu.Lock()
	if !m.wanted || m.config.Name != c.Name || m.present == present {
		m.mu.Unlock()
		return
	}
	m.present = present
	p := m.connection
	m.mu.Unlock()

	// Release the handle immediately on removal so Windows can reuse the COM
	// number when the same USB serial device returns.
	if !present && p != nil {
		_ = p.Close()
	}
	m.signal()
}

func (m *Manager) setPresent(present bool) {
	m.mu.Lock()
	m.present = present
	m.mu.Unlock()
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) drainSignal() {
	select {
	case <-m.wake:
	default:
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	mode := m.mode
	m.mu.Unlock()
	m.stopCurrent()
	if mode == "tcp" || mode == "udp" {
		m.emit(Event{StateClosed, strings.ToUpper(mode) + " 客户端已关闭"})
	} else {
		m.emit(Event{StateClosed, "串口已关闭"})
	}
}

func (m *Manager) Write(data []byte) error {
	m.mu.Lock()
	p := m.connection
	wanted := m.wanted
	mode := m.mode
	m.mu.Unlock()
	if !wanted || p == nil {
		return errors.New("连接未建立")
	}

	m.writeMu.Lock()
	n, err := io.Copy(p, bytes.NewReader(data))
	m.writeMu.Unlock()
	if n > 0 && m.onTX != nil {
		m.onTX(int(n))
	}
	if err != nil {
		_ = p.Close()
		return err
	}
	if n != int64(len(data)) {
		name := "串口"
		if mode == "tcp" || mode == "udp" {
			name = strings.ToUpper(mode)
		}
		return fmt.Errorf("%s 只发送了部分数据", name)
	}
	return nil
}

func (m *Manager) SetDTR(enabled bool) error {
	m.mu.Lock()
	p := m.serialPort
	m.config.DTR = enabled
	m.mu.Unlock()
	if p == nil {
		return errors.New("串口未连接")
	}
	return p.SetDTR(enabled)
}

func (m *Manager) SetRTS(enabled bool) error {
	m.mu.Lock()
	p := m.serialPort
	m.config.RTS = enabled
	m.mu.Unlock()
	if p == nil {
		return errors.New("串口未连接")
	}
	return p.SetRTS(enabled)
}

func (m *Manager) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connection != nil
}

func (m *Manager) emit(e Event) {
	if m.onEvent != nil {
		m.onEvent(e)
	}
}
