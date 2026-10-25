// Package bounded reads provider response bodies with a cap, so a provider (or a proxy in front
// of one) that sends an endless or enormous body costs a bounded amount of memory, not the
// process.
package bounded

import (
	"errors"
	"fmt"
	"io"
)

// ErrTooLarge is a body longer than its limit. It is not retried: the same request gets the same
// answer.
var ErrTooLarge = errors.New("response body over the limit")

// ErrorPrefix is the most of an error response's body kept for its message.
const ErrorPrefix = 4 << 10

// Read reads all of r, up to max bytes. It reads at most max+1 bytes: a body longer than max is an
// error wrapping ErrTooLarge, and nothing of it is returned.
func Read(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w of %d bytes", ErrTooLarge, max)
	}
	return data, nil
}

// Prefix is the first ErrorPrefix bytes of r, for an error message; the rest is never read.
func Prefix(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, ErrorPrefix))
	return string(data)
}
