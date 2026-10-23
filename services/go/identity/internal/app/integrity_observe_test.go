package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type outcomeLog struct {
	mu  sync.Mutex
	got []IntegrityOutcome
	why [][]IntegrityReason
}

func (l *outcomeLog) observe(o IntegrityOutcome, r []IntegrityReason) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, o)
	l.why = append(l.why, r)
}

// Every VerifyPlayIntegrity call reports exactly one outcome, and a
// restricted one its reasons.
func TestIntegrity_EveryCallReportsOneOutcome(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		v       IntegrityVerifier
		token   string
		b       IntegrityBinding
		want    IntegrityOutcome
		reasons []IntegrityReason
	}{
		{"no verifier", nil, "tok", binding(), IntegrityUnavailable, nil},
		{"invalid request", &fakeVerifier{verdict: goodVerdict()}, "", binding(), IntegrityInvalidRequest, nil},
		{"rejected", &fakeVerifier{err: ErrIntegrityTokenInvalid}, "tok", binding(), IntegrityRejected, nil},
		{"outage", &fakeVerifier{err: errors.New("503")}, "tok", binding(), IntegrityUnavailable, nil},
		{"certified", &fakeVerifier{verdict: goodVerdict()}, "tok", binding(), IntegrityCertified, nil},
		{"restricted", &fakeVerifier{verdict: func() IntegrityVerdict { v := goodVerdict(); v.AppLicensing = "UNLICENSED"; return v }()}, "tok", binding(), IntegrityRestricted, []IntegrityReason{ReasonUnlicensed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := integrityService(t, tc.v)
			log := &outcomeLog{}
			svc.SetIntegrityObserver(log.observe)
			_, _ = svc.VerifyPlayIntegrity(ctx, tc.token, tc.b)
			if len(log.got) != 1 || log.got[0] != tc.want || !slices.Equal(log.why[0], tc.reasons) {
				t.Fatalf("observed %v %v; want one %s %v", log.got, log.why, tc.want, tc.reasons)
			}
		})
	}
	// Removing the observer stops reporting and does not panic.
	svc, _ := integrityService(t, &fakeVerifier{verdict: goodVerdict()})
	svc.SetIntegrityObserver(nil)
	if _, err := svc.VerifyPlayIntegrity(ctx, "tok", binding()); err != nil {
		t.Fatal(err)
	}
}

// blockingVerifier ignores its context and never answers in time.
type blockingVerifier struct{ release chan struct{} }

func (b *blockingVerifier) Decode(context.Context, string) (IntegrityVerdict, error) {
	<-b.release
	return goodVerdict(), nil
}

// A verifier that does not answer within DecodeTimeout is unavailable, and
// the call returns about then, even if the verifier ignores its context.
// Nothing is recorded or certified.
func TestIntegrity_DecodeIsBoundedByItsTimeout(t *testing.T) {
	repo := &deviceSpy{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := NewIdentityService(repo, []byte("test_secret_key_that_is_long_enough"))
	svc.now = func() time.Time { return integrityNow }
	v := &blockingVerifier{release: make(chan struct{})}
	// Released after 2s whatever happens, so an unbounded decode fails the
	// elapsed check below instead of hanging the test.
	release := time.AfterFunc(2*time.Second, func() { close(v.release) })
	defer release.Stop()
	p := goodPolicy()
	p.DecodeTimeout = 100 * time.Millisecond
	if err := svc.SetIntegrityVerifier(v, p); err != nil {
		t.Fatal(err)
	}
	log := &outcomeLog{}
	svc.SetIntegrityObserver(log.observe)
	start := time.Now()
	a, err := svc.VerifyPlayIntegrity(context.Background(), "tok", binding())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v; the decode was not bounded by its 100ms timeout", elapsed)
	}
	if !errors.Is(err, ErrPlayIntegrityUnavailable) || a.Tier != "" || len(repo.saved) != 0 {
		t.Fatalf("(%+v, %v, %d saved); want unavailable and nothing recorded", a, err, len(repo.saved))
	}
	if len(log.got) != 1 || log.got[0] != IntegrityUnavailable {
		t.Fatalf("observed %v, want one unavailable", log.got)
	}
}

// DecodeTimeout is required and bounded.
func TestIntegrityPolicy_DecodeTimeoutIsBounded(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second, MaxDecodeTimeout + time.Millisecond} {
		p := goodPolicy()
		p.DecodeTimeout = d
		if err := p.Validate(); !errors.Is(err, ErrInvalidIntegrityPolicy) {
			t.Errorf("DecodeTimeout %v: err = %v", d, err)
		}
	}
	p := goodPolicy()
	p.DecodeTimeout = MaxDecodeTimeout
	if err := p.Validate(); err != nil {
		t.Fatalf("the maximum was refused: %v", err)
	}
}
