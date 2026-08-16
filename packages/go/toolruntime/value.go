package toolruntime

import (
	"sort"
	"strconv"
	"strings"
)

// ValueKind classifies a [Value].
type ValueKind uint8

// The value kinds. Deliberately eight, and deliberately not extensible.
//
// This is the type system tool arguments and results are expressed in, and it
// exists because the alternatives are worse. `any` gives up validation and
// deterministic fingerprinting. `map[string]string` cannot express a number a
// tool can range-check. A JSON dependency imports a parser, an encoder and a
// vendor's idea of how numbers work into the most security-sensitive module in
// the platform.
//
// Eight kinds cover every tool argument this platform has: text, whole numbers,
// quantities, flags, opaque blobs, ordered collections, keyed collections and
// absence. A ninth would need an argument better than "some tool might want it".
const (
	// ValueNull is an explicitly absent value. Distinct from a missing key: a
	// caller saying "no preferred time" and a caller forgetting to say
	// anything are different, and a tool frequently must treat them differently.
	ValueNull ValueKind = iota
	ValueString
	ValueInt
	ValueFloat
	ValueBool
	ValueBytes
	ValueList
	ValueMap
)

// String renders the kind. Used in validation messages and metric labels.
func (k ValueKind) String() string {
	switch k {
	case ValueString:
		return "string"
	case ValueInt:
		return "int"
	case ValueFloat:
		return "float"
	case ValueBool:
		return "bool"
	case ValueBytes:
		return "bytes"
	case ValueList:
		return "list"
	case ValueMap:
		return "map"
	default:
		return "null"
	}
}

// Value is one typed tool argument or result field.
//
// EVERY FIELD IS UNEXPORTED AND THERE IS NO SETTER. A Value is immutable once
// built. That is not stylistic: arguments are validated once, fingerprinted
// once, and then handed to a tool that runs concurrently with a retry of
// itself. A mutable argument means the fingerprint in the audit record may not
// describe what the tool actually received, which makes the audit record a
// story rather than a record.
type Value struct {
	kind  ValueKind
	str   string
	num   int64
	flt   float64
	flag  bool
	blob  []byte
	items []Value
	pairs map[string]Value
}

// Null returns the null value.
func Null() Value { return Value{kind: ValueNull} }

// String builds a string value.
func String(s string) Value { return Value{kind: ValueString, str: s} }

// Int builds an integer value.
func Int(n int64) Value { return Value{kind: ValueInt, num: n} }

// Float builds a floating-point value.
func Float(f float64) Value { return Value{kind: ValueFloat, flt: f} }

// Bool builds a boolean value.
func Bool(b bool) Value { return Value{kind: ValueBool, flag: b} }

// Bytes builds an opaque value. The slice is copied, so a caller mutating its
// buffer afterwards cannot change a value that has already been fingerprinted.
func Bytes(b []byte) Value {
	c := make([]byte, len(b))
	copy(c, b)
	return Value{kind: ValueBytes, blob: c}
}

// List builds an ordered collection. The slice is copied.
func List(items ...Value) Value {
	c := make([]Value, len(items))
	copy(c, items)
	return Value{kind: ValueList, items: c}
}

// Map builds a keyed collection. The map is copied.
func Map(pairs map[string]Value) Value {
	c := make(map[string]Value, len(pairs))
	for k, v := range pairs {
		c[k] = v
	}
	return Value{kind: ValueMap, pairs: c}
}

// Kind reports the value's kind.
func (v Value) Kind() ValueKind { return v.kind }

// IsNull reports whether the value is explicitly null.
func (v Value) IsNull() bool { return v.kind == ValueNull }

// Str returns the string, and false when the value is not a string.
//
// Accessors return (T, bool) rather than panicking or zero-valuing, because a
// tool reading the wrong type from an argument is a contract bug and a silent
// zero turns it into a wrong answer delivered confidently.
func (v Value) Str() (string, bool) { return v.str, v.kind == ValueString }

// Num returns the integer.
func (v Value) Num() (int64, bool) { return v.num, v.kind == ValueInt }

// Flt returns the float. An integer value converts, because a tool asking for
// a quantity should not care that the caller wrote 3 rather than 3.0.
func (v Value) Flt() (float64, bool) {
	switch v.kind {
	case ValueFloat:
		return v.flt, true
	case ValueInt:
		return float64(v.num), true
	default:
		return 0, false
	}
}

// Flag returns the boolean.
func (v Value) Flag() (bool, bool) { return v.flag, v.kind == ValueBool }

// Blob returns a COPY of the bytes.
func (v Value) Blob() ([]byte, bool) {
	if v.kind != ValueBytes {
		return nil, false
	}
	c := make([]byte, len(v.blob))
	copy(c, v.blob)
	return c, true
}

// Items returns a copy of the list.
func (v Value) Items() ([]Value, bool) {
	if v.kind != ValueList {
		return nil, false
	}
	c := make([]Value, len(v.items))
	copy(c, v.items)
	return c, true
}

// Pairs returns a copy of the map.
func (v Value) Pairs() (map[string]Value, bool) {
	if v.kind != ValueMap {
		return nil, false
	}
	c := make(map[string]Value, len(v.pairs))
	for k, val := range v.pairs {
		c[k] = val
	}
	return c, true
}

// Get looks up a key in a map value.
func (v Value) Get(key string) (Value, bool) {
	if v.kind != ValueMap {
		return Null(), false
	}
	got, ok := v.pairs[key]
	return got, ok
}

// Len returns the element count for lists, maps, strings and byte values, and
// zero for scalars.
func (v Value) Len() int {
	switch v.kind {
	case ValueString:
		return len(v.str)
	case ValueBytes:
		return len(v.blob)
	case ValueList:
		return len(v.items)
	case ValueMap:
		return len(v.pairs)
	default:
		return 0
	}
}

// SizeBytes estimates the value's serialised size, for budget accounting.
//
// An estimate rather than an exact figure, because the exact figure depends on
// a wire format this package deliberately does not choose. It is used to refuse
// oversized payloads, where being approximately right in the safe direction is
// the requirement.
func (v Value) SizeBytes() int {
	switch v.kind {
	case ValueString:
		return len(v.str)
	case ValueBytes:
		return len(v.blob)
	case ValueInt, ValueFloat, ValueBool:
		return 8
	case ValueList:
		n := 2
		for _, it := range v.items {
			n += it.SizeBytes() + 1
		}
		return n
	case ValueMap:
		n := 2
		for k, val := range v.pairs {
			n += len(k) + val.SizeBytes() + 2
		}
		return n
	default:
		return 0
	}
}

// canonical appends a deterministic byte encoding of the value.
//
// MAP KEYS ARE SORTED. That single line is what makes a fingerprint mean
// anything: without it the same arguments produce different fingerprints on
// different runs, idempotency keys stop deduplicating, and two audit records of
// the same call look like two different calls.
//
// The encoding is length-prefixed and kind-tagged so that no two distinct
// values share an encoding — otherwise a string "1" and an integer 1 would
// collide, and so would a list of two strings and one string containing the
// separator.
func (v Value) canonical(b *strings.Builder) {
	b.WriteByte(byte('0' + v.kind))
	switch v.kind {
	case ValueString:
		b.WriteString(strconv.Itoa(len(v.str)))
		b.WriteByte(':')
		b.WriteString(v.str)
	case ValueInt:
		b.WriteString(strconv.FormatInt(v.num, 10))
	case ValueFloat:
		// 'g' with -1 precision round-trips exactly, so two values that are
		// equal encode identically and two that differ do not.
		b.WriteString(strconv.FormatFloat(v.flt, 'g', -1, 64))
	case ValueBool:
		if v.flag {
			b.WriteByte('t')
		} else {
			b.WriteByte('f')
		}
	case ValueBytes:
		b.WriteString(strconv.Itoa(len(v.blob)))
		b.WriteByte(':')
		b.Write(v.blob)
	case ValueList:
		b.WriteString(strconv.Itoa(len(v.items)))
		b.WriteByte('[')
		for _, it := range v.items {
			it.canonical(b)
			b.WriteByte(',')
		}
		b.WriteByte(']')
	case ValueMap:
		keys := make([]string, 0, len(v.pairs))
		for k := range v.pairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(strconv.Itoa(len(keys)))
		b.WriteByte('{')
		for _, k := range keys {
			b.WriteString(strconv.Itoa(len(k)))
			b.WriteByte(':')
			b.WriteString(k)
			b.WriteByte('=')
			v.pairs[k].canonical(b)
			b.WriteByte(';')
		}
		b.WriteByte('}')
	}
}

// Equal reports deep equality, by comparing canonical encodings.
//
// Not reflect.DeepEqual: that would compare a Value built with Bytes(nil)
// unequal to one built with Bytes([]byte{}), which is a distinction no tool
// cares about and every test would trip over.
func (v Value) Equal(other Value) bool {
	var a, b strings.Builder
	v.canonical(&a)
	other.canonical(&b)
	return a.String() == b.String()
}

// Arguments is a tool's named inputs.
//
// A map rather than a positional list, because tool contracts evolve by adding
// optional fields and positional arguments make that a breaking change every
// time.
type Arguments map[string]Value

// Clone returns an independent copy.
func (a Arguments) Clone() Arguments {
	if a == nil {
		return nil
	}
	c := make(Arguments, len(a))
	for k, v := range a {
		c[k] = v
	}
	return c
}

// Keys returns the argument names, sorted. Sorted because every caller of this
// either logs it or hashes it, and both want stability.
func (a Arguments) Keys() []string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SizeBytes estimates the total serialised size.
func (a Arguments) SizeBytes() int {
	n := 0
	for k, v := range a {
		n += len(k) + v.SizeBytes()
	}
	return n
}

// canonicalBytes renders the arguments deterministically.
func (a Arguments) canonicalBytes() []byte {
	var b strings.Builder
	for _, k := range a.Keys() {
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte('=')
		a[k].canonical(&b)
		b.WriteByte(';')
	}
	return []byte(b.String())
}

// Fingerprint returns a stable fingerprint of the arguments.
//
// This is what goes into an audit record and an event instead of the arguments
// themselves. A phone number that a tool was called with is personal data; the
// fact that it was called with the same arguments as last time is not.
func (a Arguments) Fingerprint() Fingerprint { return fingerprintOf(a.canonicalBytes()) }

// Result is a tool's named outputs. Same shape as Arguments, deliberately: the
// output of one step is frequently the input of the next, and two types that
// differ only in name would need a converter that can only ever be the identity
// function.
type Result = Arguments
