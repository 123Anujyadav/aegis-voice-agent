package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

// CS_IDENTITY_OTP_MSG91_IPV4_ONLY: optional, exactly "true" or "false", only
// with msg91 selected, and never echoed. Nothing here contacts MSG91.

func TestMSG91IPv4Only_DefaultsToOffAndAcceptsOnlyTrueOrFalse(t *testing.T) {
	base := merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: OtpProviderMSG91}, controlsEnv(t), msg91Env(t))

	d, err := resolveOtpDelivery(lookupFrom(base))
	if err != nil {
		t.Fatal(err)
	}
	if d.MSG91IPv4Only() {
		t.Error("unset must mean off")
	}
	if _, _, err := d.otpSender(nil); err != nil {
		t.Errorf("off: building the sender: %v", err)
	}

	for value, want := range map[string]bool{"true": true, "false": false} {
		d, err := resolveOtpDelivery(lookupFrom(merge(base, map[string]string{OtpMSG91IPv4OnlyVariable: value})))
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if d.MSG91IPv4Only() != want {
			t.Errorf("%q: MSG91IPv4Only = %v", value, d.MSG91IPv4Only())
		}
	}

	// Every refused value gets the same fixed message: nothing is echoed.
	messages := map[string]bool{}
	for _, value := range []string{"1", "0", "yes", "TRUE", "True", " true", "true ", "", "on", "s3cr3t-looking-value"} {
		_, err := resolveOtpDelivery(lookupFrom(merge(base, map[string]string{OtpMSG91IPv4OnlyVariable: value})))
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%q accepted", value)
			continue
		}
		messages[err.Error()] = true
	}
	if len(messages) != 1 {
		t.Errorf("refusals differ by value (a value is echoed): %v", messages)
	}
}

func TestMSG91IPv4Only_IsRefusedWithoutMSG91(t *testing.T) {
	on := map[string]string{OtpMSG91IPv4OnlyVariable: "true"}
	for name, env := range map[string]map[string]string{
		"none":                    merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: OtpProviderNone}, on),
		"mock":                    merge(map[string]string{environmentVariable: "development", OtpProviderVariable: OtpProviderMock}, on),
		"unset provider, staging": merge(map[string]string{environmentVariable: "staging"}, on),
		"twilio alone": merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: OtpProviderTwilio},
			controlsEnv(t), twilioEnv(t), on),
	} {
		if _, err := resolveOtpDelivery(lookupFrom(env)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: accepted with %s set", name, OtpMSG91IPv4OnlyVariable)
		}
	}
	env := merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: "msg91,twilio"},
		controlsEnv(t), msg91Env(t), twilioEnv(t), on)
	d, err := resolveOtpDelivery(lookupFrom(env))
	if err != nil || !d.MSG91IPv4Only() {
		t.Errorf("msg91,twilio: %v, MSG91IPv4Only = %v", err, d.MSG91IPv4Only())
	}
}

// Fail closed: with a proxy in the environment (this package's TestMain sets
// the dead loopback proxy), IPv4-only MSG91 cannot guarantee its connections,
// so building the sender, and therefore startup, is refused.
func TestMSG91IPv4Only_StartupRefusesWhenAProxyWouldCarryMSG91(t *testing.T) {
	env := merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: OtpProviderMSG91},
		controlsEnv(t), msg91Env(t), map[string]string{OtpMSG91IPv4OnlyVariable: "true"})
	d, err := resolveOtpDelivery(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if s, _, err := d.otpSender(nil); s != nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "proxy") {
		t.Errorf("sender = %v, err = %v: want a refusal naming the proxy", s, err)
	}
}
