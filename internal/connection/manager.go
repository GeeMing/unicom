// +build windows

package connection

import (
	"errors"
	"fmt"
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
	port       *serialwin.Port
	config     serialwin.Config
	wanted     bool
	generation uint64
	onData     func([]byte)
	onEvent    func(Event)
	onTX       func(int)
}

func New(onData func([]byte), onEvent func(Event), onTX func(int)) *Manager {
	return &Manager{onData: onData, onEvent: onEvent, onTX: onTX}
}

func (m *Manager) Open(c serialwin.Config) {
	m.mu.Lock()
	if m.port != nil {
		m.port.Close()
		m.port = nil
	}
	m.config = c
	m.wanted = true
	m.generation++
	gen := m.generation
	m.mu.Unlock()
	go m.connectLoop(gen)
}

func (m *Manager) connectLoop(gen uint64) {
	delays := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}
	attempt := 0
	for {
		m.mu.Lock()
		if !m.wanted || gen != m.generation {
			m.mu.Unlock()
			return
		}
		c := m.config
		m.mu.Unlock()
		if attempt == 0 {
			m.emit(Event{StateConnecting, "正在打开 " + c.Name + "..."})
		} else {
			delay := delays[min(attempt-1, len(delays)-1)]
			m.emit(Event{StateWaiting, fmt.Sprintf("等待 %s，%.1f 秒后重试", c.Name, delay.Seconds())})
			time.Sleep(delay)
		}
		p, err := serialwin.Open(c)
		if err != nil {
			m.emit(Event{StateWaiting, err.Error()})
			attempt++
			continue
		}
		m.mu.Lock()
		if !m.wanted || gen != m.generation {
			m.mu.Unlock()
			p.Close()
			return
		}
		m.port = p
		m.mu.Unlock()
		m.emit(Event{StateConnected, c.Name + " 已连接"})
		err = m.readLoop(gen, p, c.Name)
		m.mu.Lock()
		if m.port == p {
			m.port = nil
		}
		wanted := m.wanted && gen == m.generation
		m.mu.Unlock()
		p.Close()
		if !wanted {
			return
		}
		m.emit(Event{StateWaiting, "连接中断: " + err.Error()})
		attempt = 1
	}
}

func (m *Manager) readLoop(gen uint64, p *serialwin.Port, name string) error {
	b := make([]byte, 8192)
	lastPresenceCheck := time.Now()
	for {
		n, err := p.Read(b)
		if n > 0 {
			q := make([]byte, n)
			copy(q, b[:n])
			m.onData(q)
		}
		if err != nil {
			return err
		}
		m.mu.Lock()
		active := m.wanted && gen == m.generation && m.port == p
		m.mu.Unlock()
		if !active {
			return errors.New("已关闭")
		}
		if time.Since(lastPresenceCheck) >= time.Second {
			if !serialwin.Exists(name) {
				return errors.New("设备已断开")
			}
			lastPresenceCheck = time.Now()
		}
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.wanted = false
	m.generation++
	p := m.port
	m.port = nil
	m.mu.Unlock()
	if p != nil {
		p.Close()
	}
	m.emit(Event{StateClosed, "串口已关闭"})
}

func (m *Manager) Write(b []byte) error {
	m.mu.Lock()
	p := m.port
	wanted := m.wanted
	m.mu.Unlock()
	if !wanted || p == nil {
		return errors.New("串口未连接")
	}
	n, err := p.Write(b)
	if n > 0 && m.onTX != nil {
		m.onTX(n)
	}
	if err != nil {
		p.Close()
		return err
	}
	if n != len(b) {
		return errors.New("串口只发送了部分数据")
	}
	return nil
}
func (m *Manager) Connected() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.port != nil }
func (m *Manager) emit(e Event) {
	if m.onEvent != nil {
		m.onEvent(e)
	}
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
