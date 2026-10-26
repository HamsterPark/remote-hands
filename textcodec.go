package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// textEncoding remembers how a file was stored, so edit_file can write it
// back the same way.
type textEncoding struct {
	name     string
	writable bool // edit_file may rewrite files in this encoding
	bom      bool // UTF-8 with byte order mark
	utf16    binary.ByteOrder
}

// decodeText turns file contents into a string. ok is false for files that
// look binary.
func decodeText(data []byte) (text string, enc textEncoding, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return strings.ToValidUTF8(string(data[3:]), "\uFFFD"), textEncoding{name: "utf-8 with BOM", writable: utf8.Valid(data[3:]), bom: true}, true
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return decodeUTF16(data[2:], binary.LittleEndian), textEncoding{name: "utf-16le", writable: true, utf16: binary.LittleEndian}, true
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], binary.BigEndian), textEncoding{name: "utf-16be", writable: true, utf16: binary.BigEndian}, true
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return "", textEncoding{name: "binary"}, false
	}
	if utf8.Valid(data) {
		return string(data), textEncoding{name: "utf-8", writable: true}, true
	}
	// Older Windows text files use the local code page (GBK on Chinese,
	// windows-1250 on Czech systems). Show them, but do not rewrite them.
	if s, name, ok := decodeANSI(data); ok {
		return s, textEncoding{name: name}, true
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), textEncoding{name: "invalid utf-8 (bad bytes shown as \uFFFD)"}, true
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func encodeText(s string, enc textEncoding) []byte {
	if enc.utf16 != nil {
		u := utf16.Encode([]rune(s))
		out := make([]byte, 2+2*len(u))
		enc.utf16.PutUint16(out, 0xFEFF)
		for i, v := range u {
			enc.utf16.PutUint16(out[2+2*i:], v)
		}
		return out
	}
	if enc.bom {
		return append([]byte{0xEF, 0xBB, 0xBF}, s...)
	}
	return []byte(s)
}
