package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/twilioverify"
)

// # OTP delivery
//
// Which provider delivers sign-in codes, chosen by CS_IDENTITY_OTP_PROVIDER:
//
//	mock    the development-only mock (otpdelivery.MockProvider): codes go to
//	        an in-process outbox and, if CS_IDENTITY_OTP_MOCK_OUTBOX_FILE names
//	        a file, are appended to it. ACCEPTED ONLY WHEN
//	        CS_IDENTITY_ENVIRONMENT IS SET, EXPLICITLY, TO "development". An
//	        unset ENVIRONMENT reads as development elsewhere (OD-15's known
//	        limit), so it is not enough here: a deployed pod that lost the
//	        variable must never be able to sign anyone in with a code read
//	        from an outbox.
//	twilio  Twilio Verify (internal/twilioverify), which generates, sends and
//	        checks the codes itself (app.OtpVerifier). Requires all four
//	        CS_IDENTITY_TWILIO_* variables below. NOT SELECTED ANYWHERE TODAY:
//	        no Twilio credential, Verify service or India domestic route exists
//	        for this project, and activation needs the owner's approvals first
//	        (docs/phase19/OTP-PROVIDERS.md §5, §6).
//	none    no delivery: OTP requests are refused with
//	        ERR_OTP_DELIVERY_UNAVAILABLE, as identity has always done without a
//	        provider.
//
// Unset, it is mock when ENVIRONMENT is explicitly development, and none
// otherwise, so staging and production behave exactly as before. An explicit
// mock in any other environment, an unknown value, an outbox file without the
// mock, twilio with a Twilio variable missing, or any Twilio variable without
// twilio, stops startup.
//
// The Twilio API key secret is a mounted Secret FILE, like identity's other
// secrets (readSecretFile), never an environment value. The SIDs are not
// secrets. The variables are read only when identity is activated; they never
// activate it.

// OTP delivery variables.
const (
	OtpProviderVariable       = EnvPrefix + "_OTP_PROVIDER"
	OtpMockOutboxFileVariable = EnvPrefix + "_OTP_MOCK_OUTBOX_FILE"
	environmentVariable       = EnvPrefix + "_ENVIRONMENT"

	TwilioAccountSIDVariable       = EnvPrefix + "_TWILIO_ACCOUNT_SID"
	TwilioAPIKeySIDVariable        = EnvPrefix + "_TWILIO_API_KEY_SID"
	TwilioAPIKeySecretFileVariable = EnvPrefix + "_TWILIO_API_KEY_SECRET_FILE"
	TwilioVerifyServiceSIDVariable = EnvPrefix + "_TWILIO_VERIFY_SERVICE_SID"
)

// TwilioVariables are the four Twilio variables, all required for twilio and
// all forbidden otherwise.
var TwilioVariables = []string{
	TwilioAccountSIDVariable, TwilioAPIKeySIDVariable, TwilioAPIKeySecretFileVariable, TwilioVerifyServiceSIDVariable,
}

// OTP provider names.
const (
	OtpProviderMock   = "mock"
	OtpProviderTwilio = "twilio"
	OtpProviderNone   = "none"
)

// MaxTwilioSecretFileBytes bounds the API key secret file.
const MaxTwilioSecretFileBytes = 1 << 10

// OtpDelivery is the resolved OTP delivery configuration. The zero value is
// no delivery.
type OtpDelivery struct {
	// Provider is OtpProviderMock, OtpProviderTwilio, or "" for no delivery.
	Provider string
	// MockOutboxFile is the mock's outbox file, or "".
	MockOutboxFile string
	// Twilio is the Twilio configuration, set only for OtpProviderTwilio.
	Twilio TwilioConfig
}

// TwilioConfig names the Twilio Verify configuration. The secret is a path.
type TwilioConfig struct {
	AccountSID       string
	APIKeySID        string
	APIKeySecretFile string
	VerifyServiceSID string
}

// ResolveOtpDelivery applies the rules above to the process environment.
func ResolveOtpDelivery() (OtpDelivery, error) {
	return resolveOtpDelivery(os.LookupEnv)
}

func resolveOtpDelivery(lookup func(string) (string, bool)) (OtpDelivery, error) {
	env, envSet := lookup(environmentVariable)
	explicitDevelopment := envSet && env == "development"

	values, err := readOtpVariables(lookup)
	if err != nil {
		return OtpDelivery{}, err
	}
	_, providerSet := values[OtpProviderVariable]
	provider := values[OtpProviderVariable]
	if !providerSet {
		provider = OtpProviderNone
		if explicitDevelopment {
			provider = OtpProviderMock
		}
	}
	if err := checkOtpProvider(provider, explicitDevelopment, values); err != nil {
		return OtpDelivery{}, err
	}
	switch provider {
	case OtpProviderNone:
		return OtpDelivery{}, nil // the zero value is no delivery
	case OtpProviderTwilio:
		return OtpDelivery{Provider: provider, Twilio: TwilioConfig{
			AccountSID:       values[TwilioAccountSIDVariable],
			APIKeySID:        values[TwilioAPIKeySIDVariable],
			APIKeySecretFile: values[TwilioAPIKeySecretFileVariable],
			VerifyServiceSID: values[TwilioVerifyServiceSIDVariable],
		}}, nil
	default:
		return OtpDelivery{Provider: provider, MockOutboxFile: values[OtpMockOutboxFileVariable]}, nil
	}
}

// readOtpVariables returns the OTP variables that are set, refusing blank or
// padded values. A value is never echoed.
func readOtpVariables(lookup func(string) (string, bool)) (map[string]string, error) {
	values := map[string]string{}
	for _, name := range append([]string{OtpProviderVariable, OtpMockOutboxFileVariable}, TwilioVariables...) {
		v, ok := lookup(name)
		if !ok {
			continue
		}
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%w: %s is blank", ErrInvalidConfig, name)
		}
		if strings.TrimSpace(v) != v {
			return nil, fmt.Errorf("%w: %s has leading or trailing whitespace", ErrInvalidConfig, name)
		}
		values[name] = v
	}
	return values, nil
}

// checkOtpProvider applies the selection rules to provider and the variables
// that are set.
func checkOtpProvider(provider string, explicitDevelopment bool, values map[string]string) error {
	present := func(name string) bool { _, ok := values[name]; return ok }
	switch provider {
	case OtpProviderMock:
		if !explicitDevelopment {
			return fmt.Errorf("%w: %s=%s is refused unless %s is set to development: the mock lets anyone who can read its outbox sign in",
				ErrInvalidConfig, OtpProviderVariable, OtpProviderMock, environmentVariable)
		}
	case OtpProviderTwilio:
		var missing []string
		for _, name := range TwilioVariables {
			if !present(name) {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w: %s=%s requires %s", ErrInvalidConfig, OtpProviderVariable, OtpProviderTwilio, strings.Join(missing, ", "))
		}
	case OtpProviderNone:
	default:
		// The value is an operator's word, never a secret, but it is not
		// echoed: an unknown value may be a misplaced credential.
		return fmt.Errorf("%w: %s must be %s, %s or %s", ErrInvalidConfig, OtpProviderVariable, OtpProviderMock, OtpProviderTwilio, OtpProviderNone)
	}
	if present(OtpMockOutboxFileVariable) && provider != OtpProviderMock {
		return fmt.Errorf("%w: %s is set, but the OTP provider is not the mock", ErrInvalidConfig, OtpMockOutboxFileVariable)
	}
	if provider != OtpProviderTwilio {
		for _, name := range TwilioVariables {
			if present(name) {
				return fmt.Errorf("%w: %s is set, but the OTP provider is not twilio", ErrInvalidConfig, name)
			}
		}
	}
	return nil
}

// otpSender builds the sender for d: nil, and no outbox, unless d is the mock.
func (d OtpDelivery) otpSender(observe func(string, otpdelivery.Outcome)) (*otpdelivery.Sender, *otpdelivery.Outbox, error) {
	if d.Provider != OtpProviderMock {
		return nil, nil, nil
	}
	outbox := otpdelivery.NewOutbox(d.MockOutboxFile)
	sender, err := otpdelivery.NewMockSender(outbox, observe)
	if err != nil {
		return nil, nil, err
	}
	return sender, outbox, nil
}

// otpVerifier builds the Twilio Verify verifier for d: nil unless d is
// twilio. It reads the secret file and validates every value; it makes no
// request. Errors name the variable, never a value or a path.
func (d OtpDelivery) otpVerifier(newClient func(twilioverify.Config) (*twilioverify.Client, error)) (app.OtpVerifier, error) {
	if d.Provider != OtpProviderTwilio {
		return nil, nil
	}
	raw, err := readSecretFile(TwilioAPIKeySecretFileVariable, d.Twilio.APIKeySecretFile, MaxTwilioSecretFileBytes)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	client, err := newClient(twilioverify.Config{
		AccountSID:   d.Twilio.AccountSID,
		APIKeySID:    d.Twilio.APIKeySID,
		APIKeySecret: strings.TrimSpace(string(raw)),
		ServiceSID:   d.Twilio.VerifyServiceSID,
	})
	if err != nil {
		// twilioverify's error names what is malformed, never a value.
		return nil, fmt.Errorf("%w: Twilio Verify: %w", ErrInvalidConfig, err)
	}
	return twilioVerifier{client}, nil
}

// twilioVerifier adapts a twilioverify.Client to app.OtpVerifier.
type twilioVerifier struct{ c *twilioverify.Client }

// StartVerification implements app.OtpVerifier.
func (v twilioVerifier) StartVerification(ctx context.Context, msisdn string) (string, error) {
	return v.c.StartVerification(ctx, msisdn)
}

// CheckVerification implements app.OtpVerifier. A verification Twilio no
// longer has (expired, approved or out of attempts) and a code that is not
// digits are wrong codes, counted by identity; any other failure is the
// provider's, and counts nothing.
func (v twilioVerifier) CheckVerification(ctx context.Context, ref, code string) (bool, error) {
	ok, err := v.c.CheckVerification(ctx, ref, code)
	switch {
	case err == nil:
		return ok, nil
	case errors.Is(err, twilioverify.ErrNotFound), errors.Is(err, twilioverify.ErrInvalidRequest):
		return false, nil
	default:
		return false, err
	}
}
