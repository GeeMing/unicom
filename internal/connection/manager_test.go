// +build windows

package connection

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestTCPServerAcceptsNextClient(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()

	received := make(chan []byte, 2)
	events := make(chan Event, 16)
	m := New(func(data []byte) { received <- data }, func(event Event) { events <- event }, nil)
	m.OpenTCPServer(address)
	defer m.Close()

	for clientNumber := 1; clientNumber <= 2; clientNumber++ {
		waitForState(t, events, StateWaiting)
		client, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatalf("client %d dial: %v", clientNumber, err)
		}
		waitForState(t, events, StateConnected)

		message := []byte{byte('0' + clientNumber)}
		if _, err = client.Write(message); err != nil {
			t.Fatalf("client %d write: %v", clientNumber, err)
		}
		select {
		case data := <-received:
			if !bytes.Equal(data, message) {
				t.Fatalf("client %d received %q, want %q", clientNumber, data, message)
			}
		case <-time.After(time.Second):
			t.Fatalf("client %d data timeout", clientNumber)
		}
		_ = client.Close()
	}
}

func waitForState(t *testing.T, events <-chan Event, state State) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.State == state {
				return
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for state %d", state)
		}
	}
}
