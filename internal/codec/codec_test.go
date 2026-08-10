package codec

import "testing"

func TestHexRoundTrip(t *testing.T) {
	b, err := ParseHex("01 aa FF\r\n10")
	if err != nil {
		t.Fatal(err)
	}
	if got := HexDump(b); got != "01 AA FF 10" {
		t.Fatalf("got %q", got)
	}
}
func TestHexRejectsOdd(t *testing.T) {
	if _, err := ParseHex("ABC"); err == nil {
		t.Fatal("expected error")
	}
}
func TestUTF8(t *testing.T) {
	s := "串口测试"
	b, err := Encode(s, "UTF-8")
	if err != nil || Decode(b, "UTF-8") != s {
		t.Fatalf("round trip failed: %v", err)
	}
}
func TestGBK(t *testing.T) {
	s := "中文ABC"
	b, err := Encode(s, "GBK")
	if err != nil || Decode(b, "GBK") != s {
		t.Fatalf("round trip failed: %v", err)
	}
}
func TestCompletePrefix(t *testing.T) {
	if n := CompletePrefix([]byte{0xD6, 0xD0}, "GBK"); n != 2 {
		t.Fatalf("complete GBK: %d", n)
	}
	if n := CompletePrefix([]byte{0x41, 0xD6}, "GBK"); n != 1 {
		t.Fatalf("partial GBK: %d", n)
	}
	if n := CompletePrefix([]byte("abc"), "GB18030"); n != 3 {
		t.Fatalf("ASCII GB18030: %d", n)
	}
	if n := CompletePrefix([]byte{0x81, 0x30, 0x81}, "GB18030"); n != 0 {
		t.Fatalf("partial GB18030: %d", n)
	}
	if n := CompletePrefix([]byte{0xE4, 0xB8}, "UTF-8"); n != 0 {
		t.Fatalf("partial UTF-8: %d", n)
	}
	if n := CompletePrefix([]byte{0x3D, 0xD8}, "UTF-16LE"); n != 0 {
		t.Fatalf("partial UTF-16: %d", n)
	}
}

func TestUnescape(t *testing.T) {
	got, err := Unescape(`AT\r\n\x41\u4E2D\\`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "AT\r\nA中\\" {
		t.Fatalf("got %q", got)
	}
	if _, err = Unescape(`bad\q`); err == nil {
		t.Fatal("expected unsupported escape error")
	}
	if _, err = Unescape(`bad\x1`); err == nil {
		t.Fatal("expected short hex error")
	}
}
