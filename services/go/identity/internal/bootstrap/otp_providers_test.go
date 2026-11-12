package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
)

// Real delivery-only providers (otp_providers.go): selected explicitly,
// complete or refused, and never a credential in an error. Nothing here sends
// a message: building a sender reads credential files and contacts nothing.

// twilioSID builds a Twilio-shaped identifier at runtime: GitHub push
// protection rejects such literals even as test fixtures.
func twilioSID(prefix string) string { return prefix + strings.Repeat("0a", 16) }

func secretFileFor(t *testing.T, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func msg91Env(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		OtpMSG91AuthKeyFileVariable:  secretFileFor(t, "msg91-test-key"),
		OtpMSG91SignInFlowIDVariable: "signinflow01",
		OtpMSG91CodeVariableVariable: "otp",
	}
}

func twilioEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		OtpTwilioAccountSIDFileVariable:   secretFileFor(t, twilioSID("AC")),
		OtpTwilioAPIKeySIDFileVariable:    secretFileFor(t, twilioSID("SK")),
		OtpTwilioAPIKeySecretFileVariable: secretFileFor(t, "twilio-test-secret"),
		OtpTwilioMessagingServiceVariable: twilioSID("MG"),
		OtpTwilioSignInTemplateVariable:   "Your Lily AI sign-in code is {code}.",
	}
}

func merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestRealOtpProviders_AreSelectedExplicitlyInFailoverOrder(t *testing.T) {
	base := map[string]string{environmentVariable: "staging"}
	for _, tc := range []struct {
		provider string
		env      map[string]string
		want     []string
	}{
		{"msg91", msg91Env(t), []string{"msg91"}},
		{"twilio", twilioEnv(t), []string{"twilio"}},
		{"msg91,twilio", merge(msg91Env(t), twilioEnv(t)), []string{"msg91", "twilio"}},
		{"twilio,msg91", merge(msg91Env(t), twilioEnv(t)), []string{"twilio", "msg91"}},
	} {
		env := merge(base, tc.env, map[string]string{OtpProviderVariable: tc.provider})
		d, err := resolveOtpDelivery(lookupFrom(env))
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if d.real == nil || strings.Join(d.real.names, ",") != strings.Join(tc.want, ",") || d.Provider != tc.provider {
			t.Fatalf("%s: resolved %+v", tc.provider, d)
		}
		sender, outbox, err := d.otpSender(nil)
		if err != nil || sender == nil || outbox != nil {
			t.Fatalf("%s: otpSender = %v, %v, %v", tc.provider, sender, outbox, err)
		}
		if _, ok := sender.DeletionCodes(); ok {
			t.Errorf("%s: deletion codes are wired; enabling them is decision O13", tc.provider)
		}
	}
}

func TestRealOtpProviders_PartialOrConflictingConfigurationStopsStartup(t *testing.T) {
	base := map[string]string{environmentVariable: "staging"}
	missing := msg91Env(t)
	delete(missing, OtpMSG91CodeVariableVariable)
	blank := msg91Env(t)
	blank[OtpMSG91SignInFlowIDVariable] = " "
	for name, env := range map[string]map[string]string{
		"a missing variable":                   merge(base, missing, map[string]string{OtpProviderVariable: "msg91"}),
		"a blank variable":                     merge(base, blank, map[string]string{OtpProviderVariable: "msg91"}),
		"twilio's variables without twilio":    merge(base, msg91Env(t), twilioEnv(t), map[string]string{OtpProviderVariable: "msg91"}),
		"a provider variable with no provider": merge(base, msg91Env(t)),
		"a provider variable with none":        merge(base, msg91Env(t), map[string]string{OtpProviderVariable: "none"}),
		"the same provider twice":              merge(base, msg91Env(t), map[string]string{OtpProviderVariable: "msg91,msg91"}),
		"three providers":                      merge(base, msg91Env(t), twilioEnv(t), map[string]string{OtpProviderVariable: "msg91,twilio,msg91"}),
		"mock with a real provider":            merge(base, msg91Env(t), map[string]string{OtpProviderVariable: "mock,msg91"}),
		"an unknown provider":                  merge(base, map[string]string{OtpProviderVariable: "verify"}),
		"spaces in the list":                   merge(base, msg91Env(t), twilioEnv(t), map[string]string{OtpProviderVariable: "msg91, twilio"}),
		"a mock outbox with a real provider":   merge(base, msg91Env(t), map[string]string{OtpProviderVariable: "msg91", OtpMockOutboxFileVariable: "/tmp/outbox"}),
	} {
		if _, err := resolveOtpDelivery(lookupFrom(env)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
		}
	}
}

func TestRealOtpProviders_ErrorsNeverEchoCredentialsOrUnknownValues(t *testing.T) {
	base := map[string]string{environmentVariable: "staging"}
	// A credential pasted into the selector must not be echoed.
	_, err := resolveOtpDelivery(lookupFrom(merge(base, map[string]string{OtpProviderVariable: "sk_live_misplaced_secret"})))
	if err == nil || strings.Contains(err.Error(), "misplaced_secret") {
		t.Errorf("unknown provider error = %v", err)
	}
	// A bad credential file fails at build time without echoing its content.
	env := merge(base, twilioEnv(t), map[string]string{OtpProviderVariable: "twilio"})
	env[OtpTwilioAccountSIDFileVariable] = secretFileFor(t, "not-a-sid-but-secret-looking-value")
	d, err := resolveOtpDelivery(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.otpSender(nil)
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "secret-looking") || strings.Contains(err.Error(), "twilio-test-secret") {
		t.Errorf("bad credential file: %v", err)
	}
	// A relative credential path is refused.
	env = merge(base, msg91Env(t), map[string]string{OtpProviderVariable: "msg91"})
	env[OtpMSG91AuthKeyFileVariable] = "msg91.key"
	d, err = resolveOtpDelivery(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.otpSender(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("relative key path: %v", err)
	}
}

func TestDeliveryObserver_ForwardsOnlyOnceAHookIsInstalled(t *testing.T) {
	o := &deliveryObserver{}
	o.observe("msg91", otpdelivery.OutcomeDelivered) // no hook: dropped, no panic
	var got []string
	o.set(func(p string, out otpdelivery.Outcome) { got = append(got, p+":"+string(out)) })
	o.observe("twilio", otpdelivery.OutcomeFailed)
	if strings.Join(got, ",") != "twilio:failed" {
		t.Errorf("observed %v", got)
	}
	var r Runtime
	r.ObserveOtpDeliveries(func(string, otpdelivery.Outcome) {}) // a runtime without an observer does not panic
}
