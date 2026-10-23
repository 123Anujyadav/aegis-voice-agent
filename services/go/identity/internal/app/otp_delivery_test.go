package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The OTP delivery boundary: the raw number and the code reach the sender and
// nothing else; a failed delivery withdraws the challenge; no sender leaves
// RequestOtp exactly as it was.

type recordingSender struct {
	number, code string
	calls        int
	err          error
}

func (r *recordingSender) SendOtp(_ context.Context, number, code string) error {
	r.calls++
	r.number, r.code = number, code
	return r.err
}

func TestOtpDelivery_SenderReceivesTheNumberAndTheCode(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	s := &recordingSender{}
	svc.SetOtpSender(s)
	if !svc.CanDeliverOtp() {
		t.Fatal("CanDeliverOtp is false with a sender set")
	}
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 || s.number != "+919876543210" || !codePattern(s.code) {
		t.Fatalf("sender got (%d calls, %q, %q)", s.calls, s.number, s.code)
	}
	ch, err := m.GetOtp(context.Background(), reqID)
	if err != nil || ch.CodeHash != svc.hashString(s.code) {
		t.Errorf("the delivered code is not the challenge's code: (%+v, %v)", ch, err)
	}
	if strings.Contains(ch.MSISDNPseudonym+ch.SubscriberID+ch.CodeHash, s.code) {
		t.Error("the stored challenge carries the code")
	}
}

func codePattern(c string) bool {
	if len(c) != otpDigits {
		return false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func TestOtpDelivery_FailureWithdrawsTheChallengeAndHidesTheCause(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	svc.SetOtpSender(&recordingSender{err: errors.New("provider 502 for +919876543210")})
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if !errors.Is(err, ErrOtpDeliveryFailed) || reqID != "" {
		t.Fatalf("(%q, %v), want ErrOtpDeliveryFailed and no request id", reqID, err)
	}
	if strings.Contains(err.Error(), "9876543210") || strings.Contains(err.Error(), "provider") {
		t.Errorf("the provider's error text leaked: %v", err)
	}
	if len(m.otps) != 0 {
		t.Error("an undelivered challenge is still live")
	}
	if st := m.otpRates[mp("+919876543210")]; st == nil || len(st.requests) != 1 {
		t.Error("a failed delivery did not count against the number's allowance")
	}
}

func TestOtpDelivery_NoSenderLeavesRequestOtpUnchanged(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	if svc.CanDeliverOtp() {
		t.Fatal("CanDeliverOtp is true with no sender")
	}
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil || reqID == "" || len(m.otps) != 1 {
		t.Fatalf("(%q, %v, %d challenges)", reqID, err, len(m.otps))
	}
}
