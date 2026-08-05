// +build windows

package codec

import (
	"encoding/hex"
	"errors"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	multiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	wideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
)

func CodePage(name string) uint32 {
	switch strings.ToUpper(name) {
	case "GBK":
		return 936
	case "GB18030":
		return 54936
	case "BIG5":
		return 950
	case "UTF-16LE":
		return 1200
	case "UTF-16BE":
		return 1201
	case "ASCII":
		return 20127
	default:
		return 65001
	}
}

func Decode(data []byte, encoding string) string {
	if len(data) == 0 {
		return ""
	}
	cp := CodePage(encoding)
	if cp == 1200 || cp == 1201 {
		n := len(data) / 2
		u := make([]uint16, n)
		for i := 0; i < n; i++ {
			if cp == 1200 {
				u[i] = uint16(data[i*2]) | uint16(data[i*2+1])<<8
			} else {
				u[i] = uint16(data[i*2])<<8 | uint16(data[i*2+1])
			}
		}
		return string(utf16.Decode(u))
	}
	need, _, _ := multiByteToWideChar.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0, 0)
	if need == 0 {
		return string(data)
	}
	u := make([]uint16, int(need))
	multiByteToWideChar.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&u[0])), need)
	return string(utf16.Decode(u))
}

func Encode(text, encoding string) ([]byte, error) {
	cp := CodePage(encoding)
	if cp == 65001 {
		return []byte(text), nil
	}
	u := utf16.Encode([]rune(text))
	if len(u) == 0 {
		return []byte{}, nil
	}
	if cp == 1200 || cp == 1201 {
		b := make([]byte, len(u)*2)
		for i, v := range u {
			if cp == 1200 {
				b[i*2], b[i*2+1] = byte(v), byte(v>>8)
			} else {
				b[i*2], b[i*2+1] = byte(v>>8), byte(v)
			}
		}
		return b, nil
	}
	need, _, _ := wideCharToMultiByte.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), 0, 0, 0, 0)
	if need == 0 {
		return nil, errors.New("当前系统不支持此编码")
	}
	b := make([]byte, int(need))
	usedDefault := int32(0)
	r, _, _ := wideCharToMultiByte.Call(uintptr(cp), 0, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&b[0])), need, 0, uintptr(unsafe.Pointer(&usedDefault)))
	if r == 0 {
		return nil, errors.New("编码转换失败")
	}
	if usedDefault != 0 {
		return nil, errors.New("文本中含有当前编码无法表示的字符")
	}
	return b, nil
}

func ParseHex(s string) ([]byte, error) {
	s = strings.Replace(s, " ", "", -1)
	s = strings.Replace(s, "\r", "", -1)
	s = strings.Replace(s, "\n", "", -1)
	s = strings.Replace(s, "\t", "", -1)
	if len(s)%2 != 0 {
		return nil, errors.New("HEX 必须由完整的两位字节组成")
	}
	b := make([]byte, hex.DecodedLen(len(s)))
	_, err := hex.Decode(b, []byte(s))
	if err != nil {
		return nil, errors.New("HEX 中含有非法字符")
	}
	return b, nil
}

func HexDump(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	const digits = "0123456789ABCDEF"
	b := make([]byte, 0, len(data)*3)
	for i, v := range data {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, digits[v>>4], digits[v&15])
	}
	return string(b)
}

func ValidUTF8Prefix(data []byte) int {
	if utf8.Valid(data) {
		return len(data)
	}
	for n := len(data); n >= 0 && n >= len(data)-3; n-- {
		if utf8.Valid(data[:n]) {
			return n
		}
	}
	return len(data)
}

// CompletePrefix keeps a possible trailing partial character for the next read.
func CompletePrefix(data []byte, encoding string) int {
	if len(data) == 0 {
		return 0
	}
	switch strings.ToUpper(encoding) {
	case "UTF-16LE", "UTF-16BE":
		n := len(data) - len(data)%2
		if n < 2 {
			return n
		}
		var u uint16
		if strings.ToUpper(encoding) == "UTF-16LE" {
			u = uint16(data[n-2]) | uint16(data[n-1])<<8
		} else {
			u = uint16(data[n-2])<<8 | uint16(data[n-1])
		}
		if u >= 0xD800 && u <= 0xDBFF {
			return n - 2
		}
		return n
	case "GBK", "BIG5":
		return completeDoubleByte(data)
	case "GB18030":
		return completeGB18030(data)
	case "UTF-8":
		start := len(data) - 1
		for start > 0 && data[start]&0xC0 == 0x80 {
			start--
		}
		if !utf8.FullRune(data[start:]) {
			return start
		}
		return len(data)
	default:
		return len(data)
	}
}

func completeDoubleByte(p []byte) int {
	for i := 0; i < len(p); {
		if p[i] < 0x81 || p[i] > 0xFE {
			i++
			continue
		}
		if i+1 >= len(p) {
			return i
		}
		i += 2
	}
	return len(p)
}

func completeGB18030(p []byte) int {
	for i := 0; i < len(p); {
		if p[i] < 0x81 || p[i] > 0xFE {
			i++
			continue
		}
		if i+1 >= len(p) {
			return i
		}
		if p[i+1] >= 0x30 && p[i+1] <= 0x39 {
			if i+3 >= len(p) {
				return i
			}
			i += 4
		} else {
			i += 2
		}
	}
	return len(p)
}
