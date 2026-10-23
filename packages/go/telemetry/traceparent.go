package telemetry

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// TraceID is a W3C Trace Context trace-id: 16 bytes, never all zero.
type TraceID [16]byte

// SpanID is a W3C Trace Context parent-id (span id): 8 bytes, never all zero.
type SpanID [8]byte

// IsValid reports whether the id is not all zero.
func (t TraceID) IsValid() bool { return t != TraceID{} }

// IsValid reports whether the id is not all zero.
func (s SpanID) IsValid() bool { return s != SpanID{} }

// String returns the id as 32 lower-case hex characters.
func (t TraceID) String() string { return hex.EncodeToString(t[:]) }

// String returns the id as 16 lower-case hex characters.
func (s SpanID) String() string { return hex.EncodeToString(s[:]) }

// SpanContext is the propagated part of a span: what crosses a process
// boundary in a traceparent header.
type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
	// Sampled is the traceparent "sampled" flag (bit 0 of trace-flags).
	Sampled bool
}

// IsValid reports whether both ids are valid.
func (c SpanContext) IsValid() bool { return c.TraceID.IsValid() && c.SpanID.IsValid() }

// ErrInvalidTraceparent is returned for any traceparent value that does not
// conform to W3C Trace Context Level 1 §3.2.
var ErrInvalidTraceparent = errors.New("telemetry: invalid traceparent")

// traceparentLen is the length of a version-00 traceparent:
// 2 (version) + 1 + 32 (trace-id) + 1 + 16 (parent-id) + 1 + 2 (flags).
const traceparentLen = 55

// ParseTraceparent parses a traceparent header value.
//
// It is strict, as the specification requires of a receiver: lower-case hex
// only, exact field lengths, version "ff" rejected, all-zero ids rejected.
// Version 00 must be exactly 55 characters. A higher version is parsed by its
// version-00 prefix and may only be followed by "-" and further fields, so a
// newer sender is understood without guessing at fields this code does not
// know. An invalid value is an error; the caller starts a new trace rather than
// propagating a malformed one.
func ParseTraceparent(s string) (SpanContext, error) {
	if err := checkTraceparentShape(s); err != nil {
		return SpanContext{}, err
	}
	var sc SpanContext
	var flags [1]byte
	if !decodeLowerHex(sc.TraceID[:], s[3:35]) || !sc.TraceID.IsValid() {
		return SpanContext{}, fmt.Errorf("%w: trace-id", ErrInvalidTraceparent)
	}
	if !decodeLowerHex(sc.SpanID[:], s[36:52]) || !sc.SpanID.IsValid() {
		return SpanContext{}, fmt.Errorf("%w: parent-id", ErrInvalidTraceparent)
	}
	if !decodeLowerHex(flags[:], s[53:55]) {
		return SpanContext{}, fmt.Errorf("%w: trace-flags", ErrInvalidTraceparent)
	}
	sc.Sampled = flags[0]&0x01 == 0x01
	return sc, nil
}

// checkTraceparentShape validates the length, separators and version of s, and
// what may follow the version-00 fields.
func checkTraceparentShape(s string) error {
	if len(s) < traceparentLen {
		return fmt.Errorf("%w: length %d", ErrInvalidTraceparent, len(s))
	}
	if s[2] != '-' || s[35] != '-' || s[52] != '-' {
		return fmt.Errorf("%w: field separators", ErrInvalidTraceparent)
	}
	var version [1]byte
	if !decodeLowerHex(version[:], s[0:2]) || version[0] == 0xff {
		return fmt.Errorf("%w: version", ErrInvalidTraceparent)
	}
	if version[0] == 0 && len(s) != traceparentLen {
		return fmt.Errorf("%w: version 00 has trailing data", ErrInvalidTraceparent)
	}
	if version[0] != 0 && len(s) > traceparentLen && s[traceparentLen] != '-' {
		return fmt.Errorf("%w: trailing data is not a field", ErrInvalidTraceparent)
	}
	return nil
}

// FormatTraceparent renders sc as a version-00 traceparent value. Only the
// sampled flag is emitted; the other flag bits are not defined by version 00.
func FormatTraceparent(sc SpanContext) string {
	flags := "00"
	if sc.Sampled {
		flags = "01"
	}
	return "00-" + sc.TraceID.String() + "-" + sc.SpanID.String() + "-" + flags
}

// decodeLowerHex decodes src into dst, accepting only lower-case hex digits
// (hex.DecodeString would also accept upper case, which the specification
// forbids).
func decodeLowerHex(dst []byte, src string) bool {
	if len(src) != 2*len(dst) {
		return false
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	_, err := hex.Decode(dst, []byte(src))
	return err == nil
}
