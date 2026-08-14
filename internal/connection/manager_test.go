// +build windows

package connection

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestTCPServerReceivesConcurrentClientsWithSources(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()

	received := make(chan DataEvent, 2)
	events := make(chan Event, 16)
	m := New(func(event DataEvent) { received <- event }, func(event Event) { events <- event }, nil)
	m.OpenTCPServer(address)
	defer m.Close()

	waitForState(t, events, StateWaiting)
	clients := make([]net.Conn, 0, 2)
	for clientNumber := 1; clientNumber <= 2; clientNumber++ {
		client, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatalf("client %d dial: %v", clientNumber, err)
		}
		clients = append(clients, client)
		waitForState(t, events, StateConnected)
	}
	defer func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}()

	want := make(map[string][]byte)
	for clientNumber, client := range clients {
		message := []byte{byte('0' + clientNumber)}
		message[0]++
		if _, err := client.Write(message); err != nil {
			t.Fatalf("client %d write: %v", clientNumber, err)
		}
		want[client.LocalAddr().String()] = message
	}
	for range clients {
		select {
		case event := <-received:
			message, ok := want[event.Source]
			if !ok {
				t.Fatalf("unexpected source %q", event.Source)
			}
			if !bytes.Equal(event.Data, message) {
				t.Fatalf("source %s received %q, want %q", event.Source, event.Data, message)
			}
			delete(want, event.Source)
		case <-time.After(time.Second):
			t.Fatal("TCP data timeout")
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing TCP sources: %v", want)
	}

	broadcast := []byte("server")
	if err := m.Write(broadcast); err != nil {
		t.Fatalf("server broadcast: %v", err)
	}
	for i, client := range clients {
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		got := make([]byte, len(broadcast))
		if _, err := io.ReadFull(client, got); err != nil {
			t.Fatalf("client %d broadcast read: %v", i+1, err)
		}
		if !bytes.Equal(got, broadcast) {
			t.Fatalf("client %d broadcast = %q, want %q", i+1, got, broadcast)
		}
	}
}

func TestUDPReceivesMultipleSources(t *testing.T) {
	localProbe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	localAddress := localProbe.LocalAddr().String()
	_ = localProbe.Close()

	remote, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	received := make(chan DataEvent, 2)
	events := make(chan Event, 16)
	m := New(func(event DataEvent) { received <- event }, func(event Event) { events <- event }, nil)
	m.OpenUDP(localAddress, remote.LocalAddr().String())
	defer m.Close()
	waitForState(t, events, StateConnected)

	target, err := net.ResolveUDPAddr("udp", localAddress)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string][]byte)
	var senders []*net.UDPConn
	for i := 0; i < 2; i++ {
		sender, listenErr := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if listenErr != nil {
			t.Fatal(listenErr)
		}
		senders = append(senders, sender)
		message := []byte{byte('A' + i)}
		want[sender.LocalAddr().String()] = message
		if _, err = sender.WriteToUDP(message, target); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, sender := range senders {
			_ = sender.Close()
		}
	}()

	for range senders {
		select {
		case event := <-received:
			message, ok := want[event.Source]
			if !ok || !bytes.Equal(event.Data, message) {
				t.Fatalf("UDP event = {%q %q}, want matching source and data", event.Source, event.Data)
			}
			delete(want, event.Source)
		case <-time.After(time.Second):
			t.Fatal("UDP data timeout")
		}
	}

	reply := []byte("reply")
	if err = m.Write(reply); err != nil {
		t.Fatalf("UDP default remote write: %v", err)
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(reply))
	if _, _, err = remote.ReadFromUDP(got); err != nil {
		t.Fatalf("UDP default remote read: %v", err)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("UDP default remote data = %q, want %q", got, reply)
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
