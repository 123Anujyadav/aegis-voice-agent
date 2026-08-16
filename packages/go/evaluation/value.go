package evaluation

import (
	"sort"
	"strconv"
	"strings"
)

// ValueKind classifies a [Value].
type ValueKind uint8

// The value kinds. Four, and deliberately no nesting.
//
// THIS IS THE THIRD CANONICAL VALUE TYPE IN THE PLATFORM — Phase 10D has one for
// tool arguments, Phase 10E has one for policy attributes, and this is a third.
// That is a real duplication and it is recorded as a finding rather than
// explained away; see ENGINEERING_AUDIT §A2.
//
// It exists because the alternative is worse in a specific way: importing 10D's
// or 10E's type would make this module depend on something it evaluates, which
// is the one thing the platform's whole design refuses. A shared value package
// under packages/go/platform is the correct fix and it is a change to a frozen
// module.
//
// Four kinds, because a scenario step's arguments and a subsystem's observable
// outputs are identifiers, codes, counts and flags. Anything structured is
// fingerprinted by the adapter and compared as a string — which is also what
// keeps an observation readable in a drift report.
const (
	ValueAbsent ValueKind = iota
	ValueString
	ValueNumber
	ValueBool
)

// String renders the kind.
func (k ValueKind) String() string {
	switch k {
	case ValueString:
		return "string"
	case ValueNumber:
		return "number"
	case ValueBool:
		return "bool"
	default:
		return "absent"
	}
}

// Value is one typed scenario argument or observed output.
//
// Immutable by construction: unexported fields, no setters. A value that could
// change after an observation was fingerprinted would make the golden a story
// rather than a record.
type Value struct {
	kind ValueKind
	str  string
	num  float64
	flag bool
}

// S builds a string value.
func S(s string) Value { return Value{kind: ValueString, str: s} }

// N builds a numeric value.
func N(f float64) Value { return Value{kind: ValueNumber, num: f} }

// B builds a boolean value.
func B(b bool) Value { return Value{kind: ValueBool, flag: b} }

// Absent is the explicitly-absent value. Distinct from a missing key: "the
// subsystem reported no answer" and "the adapter forgot to report" are different
// observations, and a golden must be able to tell them apart.
func Absent() Value { return Value{kind: ValueAbsent} }

// Kind reports the value's kind.
func (v Value) Kind() ValueKind { return v.kind }

// IsAbsent reports explicit absence.
func (v Value) IsAbsent() bool { return v.kind == ValueAbsent }

// Str returns the string, and false when the value is not a string.
func (v Value) Str() (string, bool) { return v.str, v.kind == ValueString }

// Num returns the number.
func (v Value) Num() (float64, bool) { return v.num, v.kind == ValueNumber }

// Flag returns the boolean.
func (v Value) Flag() (bool, bool) { return v.flag, v.kind == ValueBool }

// Display renders the value for a report. Never used for comparison.
func (v Value) Display() string {
	switch v.kind {
	case ValueString:
		return v.str
	case ValueNumber:
		return strconv.FormatFloat(v.num, 'g', -1, 64)
	case ValueBool:
		return strconv.FormatBool(v.flag)
	default:
		return "<absent>"
	}
}

// canonical appends a deterministic encoding.
//
// Kind-tagged and length-prefixed so no two distinct values share an encoding.
// Without that, the string "1" and the number 1 collide, and a behaviour
// fingerprint that cannot tell them apart cannot detect the day a subsystem
// started returning one where it used to return the other.
func (v Value) canonical(b *strings.Builder) {
	b.WriteByte(byte('0' + v.kind))
	switch v.kind {
	case ValueString:
		b.WriteString(strconv.Itoa(len(v.str)))
		b.WriteByte(':')
		b.WriteString(v.str)
	case ValueNumber:
		b.WriteString(strconv.FormatFloat(v.num, 'g', -1, 64))
	case ValueBool:
		if v.flag {
			b.WriteByte('t')
		} else {
			b.WriteByte('f')
		}
	}
}

// Equal reports equality by canonical encoding.
func (v Value) Equal(other Value) bool {
	var a, b strings.Builder
	v.canonical(&a)
	other.canonical(&b)
	return a.String() == b.String()
}

// Values is a named value set.
type Values map[string]Value

// Clone returns an independent copy.
func (v Values) Clone() Values {
	if v == nil {
		return nil
	}
	c := make(Values, len(v))
	for k, val := range v {
		c[k] = val
	}
	return c
}

// Keys returns the names, sorted. Sorted because every caller either encodes it
// or renders it, and both want stability.
func (v Values) Keys() []string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get returns a value, or Absent when the key is missing.
func (v Values) Get(key string) Value {
	if got, ok := v[key]; ok {
		return got
	}
	return Absent()
}

// Lookup returns a value and whether the key was present at all.
func (v Values) Lookup(key string) (Value, bool) {
	got, ok := v[key]
	return got, ok
}

// Num reads a numeric value, defaulting to zero.
func (v Values) Num(key string) float64 {
	n, _ := v.Get(key).Num()
	return n
}

// Str reads a string value, defaulting to empty.
func (v Values) Str(key string) string {
	s, _ := v.Get(key).Str()
	return s
}

// canonical appends the deterministic encoding, with sorted keys.
func (v Values) canonical(b *strings.Builder) {
	for _, k := range v.Keys() {
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte('=')
		v[k].canonical(b)
		b.WriteByte(';')
	}
}

// Fingerprint returns a stable fingerprint of the value set.
func (v Values) Fingerprint() Fingerprint {
	var b strings.Builder
	v.canonical(&b)
	return fingerprintOf([]byte(b.String()))
}

// Diff returns the keys that differ between two value sets, sorted.
//
// Reported as keys rather than as a full structural diff because a drift report
// is read by a person deciding whether a change was intended, and "the answer
// field changed" is the useful unit. The values themselves are available on both
// observations for anybody who wants them.
func (v Values) Diff(other Values) []string {
	seen := make(map[string]bool, len(v)+len(other))
	for k := range v {
		seen[k] = true
	}
	for k := range other {
		seen[k] = true
	}

	var out []string
	for k := range seen {
		if !v.Get(k).Equal(other.Get(k)) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
