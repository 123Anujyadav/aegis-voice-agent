package bootstrap

import (
	"testing"
)

// A provider's request id reaches the installed hook labelled with the
// provider; before a hook is installed it is dropped; a nil observer gives the
// adapters no hooks at all.
func TestDeliveryObserver_ForwardsAcceptedRequestIDs(t *testing.T) {
	var got [][2]string
	o := &deliveryObserver{}
	report := o.acceptedBy(OtpProviderMSG91)
	report("dropped0before0hook") // no hook yet: dropped, no panic
	o.setAccepted(func(provider, id string) { got = append(got, [2]string{provider, id}) })
	report("5e1e93cad6fc054d8e759a5b")
	if len(got) != 1 || got[0] != [2]string{OtpProviderMSG91, "5e1e93cad6fc054d8e759a5b"} {
		t.Errorf("hook received %v", got)
	}

	var none *deliveryObserver
	if none.acceptedBy(OtpProviderMSG91) != nil || none.outcomes() != nil {
		t.Error("a nil observer must give no hooks")
	}
}

// The runtime installs the hook on its observer; a runtime without one ignores
// it.
func TestRuntime_ObserveOtpAcceptances(t *testing.T) {
	var got string
	r := &Runtime{otpObserver: &deliveryObserver{}}
	r.ObserveOtpAcceptances(func(_, id string) { got = id })
	r.otpObserver.acceptedBy(OtpProviderMSG91)("abc123")
	if got != "abc123" {
		t.Errorf("got %q", got)
	}
	(&Runtime{}).ObserveOtpAcceptances(func(string, string) {}) // no observer: no panic
}
