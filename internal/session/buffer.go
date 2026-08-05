package session

import "sync"

type Buffer struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	received uint64
}

func NewBuffer(limit int) *Buffer { return &Buffer{limit: limit} }
func (b *Buffer) Append(p []byte) {
	b.mu.Lock()
	b.received += uint64(len(p))
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		copy(b.data, b.data[len(b.data)-b.limit:])
		b.data = b.data[:b.limit]
	}
	b.mu.Unlock()
}
func (b *Buffer) Snapshot() ([]byte, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := make([]byte, len(b.data))
	copy(p, b.data)
	return p, b.received
}
func (b *Buffer) Received() uint64 { b.mu.Lock(); defer b.mu.Unlock(); return b.received }
func (b *Buffer) Delta(offset uint64) (p []byte, total uint64, reset bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	earliest := b.received - uint64(len(b.data))
	if offset < earliest || offset > b.received {
		offset = earliest
		reset = true
	}
	start := int(offset - earliest)
	p = make([]byte, len(b.data)-start)
	copy(p, b.data[start:])
	return p, b.received, reset
}
func (b *Buffer) Clear() { b.mu.Lock(); b.data = b.data[:0]; b.received = 0; b.mu.Unlock() }
