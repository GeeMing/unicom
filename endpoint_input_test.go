package main

import "testing"

func TestNormalizeIPInput(t *testing.T) {
	got := normalizeIPInput(" 192\uff0e168\uff0e1\uff0e20\uff1a\t9000\r\n")
	want := "192.168.1.20:9000"
	if got != want {
		t.Fatalf("normalizeIPInput() = %q, want %q", got, want)
	}
}

func TestNormalizeEndpointInput(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		host, port string
		hasPort    bool
	}{
		{name: "whitespace", input: " \t127.0.0.1\r\n ", host: "127.0.0.1"},
		{name: "host and port", input: " 192.168.1.20 :\t8080\r\n", host: "192.168.1.20", port: "8080", hasPort: true},
		{name: "full width punctuation", input: "192\uff0e168\uff0e1\uff0e20\uff1a9000", host: "192.168.1.20", port: "9000", hasPort: true},
		{name: "ideographic full stop", input: "10\u30020\u30020\u30021", host: "10.0.0.1"},
		{name: "port is not present yet", input: "127.0.0.1:", host: "127.0.0.1:"},
		{name: "unbracketed IPv6", input: "::1", host: "::1"},
		{name: "bracketed IPv6 and port", input: "[::1]:443", host: "[::1]", port: "443", hasPort: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, hasPort := normalizeEndpointInput(tt.input)
			if host != tt.host || port != tt.port || hasPort != tt.hasPort {
				t.Fatalf("normalizeEndpointInput(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.input, host, port, hasPort, tt.host, tt.port, tt.hasPort)
			}
		})
	}
}
