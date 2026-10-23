package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// With no service-specific configuration this service starts and serves
// health, so its whole normal startup can be observed.
func startupEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
}

func checkStartup(t *testing.T, healthPort int, logs *lockedBuffer) {
	t.Helper()
	if v := readyVersion(t, healthPort); v != "dev" {
		t.Errorf("/readyz version = %q; want the honest default, not the environment's or the malformed value", v)
	}
	time.Sleep(100 * time.Millisecond) // let the startup log lines flush
	wantBuildLogs(t, logs.String())
}

// readyVersion polls /readyz until the service answers and returns the
// version it reports. It lives here, not in buildinfo_test.go, because only
// services that serve health without extra configuration can use it.
func readyVersion(t *testing.T, healthPort int) string {
	t.Helper()
	for i := 0; i < 150; i++ {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", healthPort))
		if err == nil {
			var body struct {
				Version string `json:"version"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			return body.Version
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the service never answered /readyz")
	return ""
}
