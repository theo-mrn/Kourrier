// Package id generates time-ordered unique identifiers (UUIDv7, RFC 9562).
package id

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// New returns a new UUIDv7 in its canonical textual form. UUIDv7 values sort
// by creation time, which keeps logs and Redis keys naturally ordered.
func New() string {
	var u [16]byte
	_, _ = rand.Read(u[6:])
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(u[0:8], ms<<16|uint64(binary.BigEndian.Uint16(u[6:8])))
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant

	var buf [36]byte
	hex.Encode(buf[0:8], u[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], u[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], u[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], u[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:], u[10:])
	return string(buf[:])
}

// Valid reports whether s looks like a canonical UUID.
func Valid(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
