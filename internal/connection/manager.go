// +build windows

package connection

import (
	"errors"
	"fmt"
	"sync"

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
	port       *serialwin.Port
	config     serialwin.Config
	wanted     bool
	present    bool
	generation uint64
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
	m.wanted = true
	m.present = serialwin.Exists(c.Name)
	m.generation++
	gen := m.generation
	m.wg.Add(1)
	m.mu.Unlock()

	go m.connectLoop(gen)
}

func (m *Manager) stopCurrent() {
	m.mu.Lock()
	m.wanted = false
	m.generation++
	p := m.port
	m.port = nil
	m.mu.Unlock()

	if p != nil {
		_ = p.Close()
	}
	m.signal()
	m.wg.Wait()
}

func (m *Manager) connectLoop(gen uint64) {
	defer m.wg.Done()

	for {
		m.mu.Lock()
		if !m.wanted || gen != m.generation {
			m.mu.Unlock()
			return
		}
		c := m.config
		m.mu.Unlock()

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
		m.port = p
		m.mu.Unlock()
		m.emit(Event{StateConnected, c.Name + " 已连接"})

		err = m.readLoop(gen, p)
		m.mu.Lock()
		if m.port == p {
			m.port = nil
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

func (m *Manager) readLoop(gen uint64, p *serialwin.Port) error {
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
		active := m.wanted && gen == m.generation && m.port == p
		m.mu.Unlock()
		if !active {
			return errors.New("串口已关闭")
		}
	}
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
	wasPresent := m.present
	m.mu.Unlock()

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
	p := m.port
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
	m.stopCurrent()
	m.emit(Event{StateClosed, "串口已关闭"})
}

func (m *Manager) Write(data []byte) error {
	m.mu.Lock()
	p := m.port
	wanted := m.wanted
	m.mu.Unlock()
	if !wanted || p == nil {
		return errors.New("串口未连接")
	}

	n, err := p.Write(data)
	if n > 0 && m.onTX != nil {
		m.onTX(n)
	}
	if err != nil {
		_ = p.Close()
		return err
	}
	if n != len(data) {
		return errors.New("串口只发送了部分数据")
	}
	return nil
}

func (m *Manager) SetDTR(enabled bool) error {
	m.mu.Lock()
	p := m.port
	m.config.DTR = enabled
	m.mu.Unlock()
	if p == nil {
		return errors.New("串口未连接")
	}
	return p.SetDTR(enabled)
}

func (m *Manager) SetRTS(enabled bool) error {
	m.mu.Lock()
	p := m.port
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
	return m.port != nil
}

func (m *Manager) emit(e Event) {
	if m.onEvent != nil {
		m.onEvent(e)
	}
}
