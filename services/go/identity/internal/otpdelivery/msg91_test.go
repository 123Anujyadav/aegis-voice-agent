package otpdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// MSG91 against the "Send SMS" contract verified from MSG91's documentation
// on 2026-11-13 (docs/phase19/MSG91-INTEGRATION.md §3), on a local TLS
// stand-in. Nothing here contacts MSG91.

func TestMSG91_MatchesTheVerifiedSendSMSContract(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, `{"type":"success","message":"5e1e93cad6fc054d8e759a5b"}`)
	p, err := NewMSG91(msg91Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SendOtp(context.Background(), indiaNumber, adapterCode); err != nil {
		t.Fatalf("SendOtp: %v", err)
	}
	r := c.requests[0]
	for header, want := range map[string]string{
		"authkey": "msg91-test-auth-key", "content-type": "application/json", "accept": "application/json",
	} {
		if got := r.Header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(c.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(body); strings.Join(got, ",") != "realTimeResponse,recipients,short_url,template_id" {
		t.Errorf("body fields = %v: only the documented fields are sent", got)
	}
	if string(body["realTimeResponse"]) != `"1"` || string(body["short_url"]) != `"0"` {
		t.Errorf("realTimeResponse = %s, short_url = %s", body["realTimeResponse"], body["short_url"])
	}
	var recipients []map[string]string
	if err := json.Unmarshal(body["recipients"], &recipients); err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || len(recipients[0]) != 2 ||
		recipients[0]["mobiles"] != "919876543210" || recipients[0]["otp"] != adapterCode {
		t.Errorf("recipients = %s: one recipient, the number with 91 and no +, and the code variable only", body["recipients"])
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestMSG91_ReportsTheRequestIDOnlyWhenWellFormed(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		wantErr      error
		wantID       string
	}{
		{"documented success", `{"type":"success","message":"5e1e93cad6fc054d8e759a5b"}`, nil, "5e1e93cad6fc054d8e759a5b"},
		{"success without an id", `{"type":"success"}`, nil, ""},
		{"success with text, not an id", `{"type":"success","message":"sent to 919876543210 code 135790"}`, nil, ""},
		{"documented error in a 200", `{"type":"error","message":"The provided flow ID or template ID is invalid."}`, ErrRejected, ""},
		{"no number processed", `{"type":"error","message":"Please enter atleast one number to send sms."}`, ErrRejected, ""},
		{"not JSON", `ok`, ErrRejected, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newCapture(t, http.StatusOK, tc.answer)
			var (
				mu  sync.Mutex
				ids []string
			)
			cfg := msg91Config(t, srv)
			cfg.Accepted = func(id string) {
				mu.Lock()
				ids = append(ids, id)
				mu.Unlock()
			}
			p, err := NewMSG91(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.SendOtp(context.Background(), indiaNumber, adapterCode); !errors.Is(err, tc.wantErr) {
				t.Fatalf("SendOtp = %v, want %v", err, tc.wantErr)
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case tc.wantID == "" && len(ids) != 0:
				t.Errorf("reported %q: only a well-formed id of an accepted message is reported", ids)
			case tc.wantID != "" && (len(ids) != 1 || ids[0] != tc.wantID):
				t.Errorf("reported %q, want exactly [%s]", ids, tc.wantID)
			}
		})
	}
}

// One SendOtp is one request, whatever the answer: the adapter never retries,
// so it can never send the same code twice by itself.
func TestMSG91_OneRequestPerCallNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		answer string
		hold   bool
	}{
		{"server error", http.StatusInternalServerError, `{"type":"error"}`, false},
		{"rate limited", http.StatusTooManyRequests, `{"type":"error"}`, false},
		{"unauthorised", http.StatusUnauthorized, `{"type":"error","message":"Auth Key missing"}`, false},
		{"error in a 200", http.StatusOK, `{"type":"error"}`, false},
		{"no answer before the deadline", http.StatusOK, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := newCapture(t, tc.status, tc.answer)
			c.mu.Lock()
			c.hold = tc.hold
			c.mu.Unlock()
			p, err := NewMSG91(msg91Config(t, srv))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if err := p.SendOtp(ctx, indiaNumber, adapterCode); err == nil {
				t.Fatal("a refused or unanswered send reported success")
			}
			if n := c.count(); n != 1 {
				t.Errorf("%d requests for one code, want exactly 1", n)
			}
		})
	}
}

// MSG91 as the only provider (the M1 selection): an attempt that times out is
// not followed by any other request, so the subscriber can receive the code
// at most once.
func TestMSG91_AloneInTheSenderIsAtMostOneRequestPerCode(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, "")
	c.mu.Lock()
	c.hold = true
	c.mu.Unlock()
	p, err := NewMSG91(msg91Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []Outcome
	s, err := New(Config{
		Providers: []Provider{p}, AttemptTimeout: MinAttemptTimeout,
		Observe: func(_ string, o Outcome) { outcomes = append(outcomes, o) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendOtp(context.Background(), indiaNumber, adapterCode); !errors.Is(err, ErrAllProvidersFailed) {
		t.Fatalf("SendOtp = %v, want ErrAllProvidersFailed", err)
	}
	if n := c.count(); n != 1 {
		t.Errorf("%d requests, want exactly 1", n)
	}
	if len(outcomes) != 1 || outcomes[0] != OutcomeTimeout {
		t.Errorf("outcomes = %v, want one timeout", outcomes)
	}
}
