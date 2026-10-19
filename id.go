package yuruidb

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

var generator = struct {
	sync.Mutex
	last   [16]byte
	millis int64
}{}

// NewID creates an RFC 9562 UUIDv7. IDs are monotonic within this process even
// if its wall clock moves backward. Independent processes are not coordinated.
func NewID() (string, error) {
	generator.Lock()
	defer generator.Unlock()
	ms := time.Now().UnixMilli()
	var b [16]byte
	if ms > generator.millis {
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		for i := 5; i >= 0; i-- {
			b[i] = byte(ms)
			ms >>= 8
		}
		b[6] = b[6]&15 | 0x70
		b[8] = b[8]&63 | 0x80
	} else {
		b = generator.last
		carry := true
		for i := 15; i >= 6 && carry; i-- {
			mask := byte(255)
			if i == 8 {
				mask = 63
			}
			if i == 6 {
				mask = 15
			}
			n := b[i] & mask
			b[i] = b[i]&^mask | (n+1)&mask
			carry = n == mask
		}
		if carry {
			return "", fmt.Errorf("%w: UUID sequence exhausted", ErrInvalid)
		}
	}
	generator.last = b
	generator.millis = int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 | int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func ValidateID(id string) error {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '7' || !strings.ContainsRune("89ab", rune(id[19])) || strings.ToLower(id) != id {
		return fmt.Errorf("%w: canonical lowercase UUIDv7 required", ErrInvalid)
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(id, "-", "")); err != nil {
		return fmt.Errorf("%w: malformed UUID", ErrInvalid)
	}
	return nil
}
func NewRevision() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidateCollection accepts a portable, single-component collection name.
func ValidateCollection(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return fmt.Errorf("%w: collection length must be 1..128", ErrInvalid)
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return fmt.Errorf("%w: invalid collection name", ErrInvalid)
		}
	}
	return nil
}
