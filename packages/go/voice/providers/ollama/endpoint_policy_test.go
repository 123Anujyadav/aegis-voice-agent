package ollama

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// RATIFIED POLICY PINS.
//
// docs/phase19/OLLAMA-ENDPOINT-POLICY.md was ratified by the owner on
// 2026-11-10, exactly as proposed. These tests fail if the code drifts from it
// in either direction. Widening it (another host, an override, a proxy, a
// redirect) is a separate, high-risk decision that needs the owner and the
// OD-12 review first; it is never a test edit.

const policyDoc = "../../../../../docs/phase19/OLLAMA-ENDPOINT-POLICY.md"

// ratifiedHosts is the allowed set, exactly as ratified.
var ratifiedHosts = []string{"127.0.0.1", "::1", "localhost"}

func TestRatifiedPolicy_TheAllowedHostsAreExactlyTheRatifiedThree(t *testing.T) {
	var got []string
	for h, ok := range loopbackHosts {
		if ok {
			got = append(got, h)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, ratifiedHosts) {
		t.Fatalf("allowed hosts = %v; the ratified policy allows exactly %v", got, ratifiedHosts)
	}
}

func TestRatifiedPolicy_LANAndPublicEndpointsAreRefused(t *testing.T) {
	for _, base := range []string{
		"http://172.16.0.1:11434",        // RFC 1918
		"http://169.254.1.1:11434",       // link-local
		"http://[fe80::1]:11434",         // IPv6 link-local
		"http://[fd00::1]:11434",         // IPv6 unique local
		"http://8.8.8.8:11434",           // public IPv4
		"http://[2001:4860::8888]:11434", // public IPv6
		"http://ollama.local:11434",      // mDNS name
		"https://ollama.example.com",     // a hosted endpoint
	} {
		if _, err := New(endpointConfig(base)); err == nil {
			t.Errorf("%q was accepted", base)
		}
	}
}

// No override exists: nothing in the adapter's own code reads the environment,
// so no variable can widen the endpoint, add a proxy or allow a redirect.
func TestRatifiedPolicy_NothingReadsTheEnvironment(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"os.Getenv", "os.LookupEnv", "os.Environ", "ProxyFromEnvironment"} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s uses %s: the ratified policy has no override", f, call)
			}
		}
	}
}

// A new Config field is the likeliest way to add an override. Each one must be
// checked against the ratified policy before it is added here.
func TestRatifiedPolicy_ConfigHasNoUnreviewedField(t *testing.T) {
	reviewed := []string{
		"ID", "BaseURL", "Model", "MaxContextTokens", "MaxOutputTokens",
		"RequestTimeout", "ProbeTimeout", "MaxChunkBytes", "HTTPClient",
	}
	ty := reflect.TypeOf(Config{})
	got := make([]string, 0, ty.NumField())
	for i := 0; i < ty.NumField(); i++ {
		got = append(got, ty.Field(i).Name)
	}
	if !slices.Equal(got, reviewed) {
		t.Fatalf("Config fields = %v, reviewed = %v. A new field must not widen the endpoint, "+
			"add a proxy or allow a redirect (OLLAMA-ENDPOINT-POLICY.md, ratified); "+
			"check it against the policy, then add it to this list", got, reviewed)
	}
}

func TestRatifiedPolicy_EveryClientRefusesRedirectsAndTheOwnClientHasNoProxy(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:11434/api/tags", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*http.Client{
		"own":      guardedClient(nil),
		"supplied": guardedClient(&http.Client{}),
	} {
		if c.CheckRedirect == nil || c.CheckRedirect(req, nil) == nil {
			t.Errorf("%s client: a redirect would be followed", name)
		}
	}
	if tr, ok := guardedClient(nil).Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Error("the adapter's own client could use a proxy")
	}
}

// The record and the code agree: the document is marked ratified, and names
// every allowed host.
func TestRatifiedPolicy_TheDocumentMatchesTheCode(t *testing.T) {
	doc, err := os.ReadFile(policyDoc)
	if err != nil {
		t.Fatalf("the ratified policy document is missing: %v", err)
	}
	text := string(doc)
	if !strings.Contains(text, "Ratified by the owner on 2026-11-10") {
		t.Error("the policy document does not record the owner's ratification")
	}
	for _, h := range ratifiedHosts {
		if !strings.Contains(text, "`"+h+"`") {
			t.Errorf("the policy document does not name the allowed host %q", h)
		}
	}
}
