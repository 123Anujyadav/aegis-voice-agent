package telemetry

import (
	"errors"
	"strings"
	"testing"
)

// The W3C Trace Context Level 1 specification's own example value (§3.2.2.5
// and the test suite's canonical header).
const specExample = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func TestParseTraceparent_SpecExample(t *testing.T) {
	t.Parallel()
	sc, err := ParseTraceparent(specExample)
	if err != nil {
		t.Fatal(err)
	}
	if sc.TraceID.String() != "0af7651916cd43dd8448eb211c80319c" || sc.SpanID.String() != "b7ad6b7169203331" || !sc.Sampled {
		t.Errorf("parsed %+v", sc)
	}
	if got := FormatTraceparent(sc); got != specExample {
		t.Errorf("round trip = %q, want %q", got, specExample)
	}
}

func TestParseTraceparent_FlagsAndFutureVersions(t *testing.T) {
	t.Parallel()
	unsampled := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"
	sc, err := ParseTraceparent(unsampled)
	if err != nil || sc.Sampled {
		t.Fatalf("%q = %+v, %v", unsampled, sc, err)
	}
	if got := FormatTraceparent(sc); got != unsampled {
		t.Errorf("round trip = %q", got)
	}
	// Bits other than "sampled" are undefined in version 00; they do not set it.
	if sc, err := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-02"); err != nil || sc.Sampled {
		t.Errorf("flags 02 = %+v, %v", sc, err)
	}
	// A higher version is read by its version-00 prefix, with or without
	// further fields.
	for _, v := range []string{
		"cc-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"cc-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-what-the-future-will-be-like",
	} {
		if _, err := ParseTraceparent(v); err != nil {
			t.Errorf("%q rejected: %v", v, err)
		}
	}
}

func TestParseTraceparent_RejectsInvalidValues(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty":                    "",
		"version ff":               "ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"upper-case trace-id":      "00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01",
		"upper-case parent-id":     "00-0af7651916cd43dd8448eb211c80319c-B7AD6B7169203331-01",
		"upper-case version":       "0A-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"all-zero trace-id":        "00-00000000000000000000000000000000-b7ad6b7169203331-01",
		"all-zero parent-id":       "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
		"short trace-id":           "00-0af7651916cd43dd8448eb211c8031-b7ad6b7169203331-01",
		"long parent-id":           "00-0af7651916cd43dd8448eb211c80319c-b7ad6b716920333100-01",
		"non-hex":                  "00-0af7651916cd43dd8448eb211c80319g-b7ad6b7169203331-01",
		"bad separator":            "00_0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"version 00 trailing data": specExample + "-extra",
		"future version, no dash":  "cc-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01x",
		"one-char flags":           "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-1",
		"oversized":                strings.Repeat("0", 8192),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if sc, err := ParseTraceparent(v); !errors.Is(err, ErrInvalidTraceparent) {
				t.Errorf("%q = %+v, %v; want ErrInvalidTraceparent", v, sc, err)
			}
		})
	}
}
