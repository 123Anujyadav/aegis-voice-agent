package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
)

// # OTP delivery
//
// Which provider delivers sign-in codes, chosen by CS_IDENTITY_OTP_PROVIDER:
//
//	mock  the development-only mock (otpdelivery.MockProvider): codes go to an
//	      in-process outbox and, if CS_IDENTITY_OTP_MOCK_OUTBOX_FILE names a
//	      file, are appended to it. ACCEPTED ONLY WHEN CS_IDENTITY_ENVIRONMENT
//	      IS SET, EXPLICITLY, TO "development". An unset ENVIRONMENT reads as
//	      development elsewhere (OD-15's known limit), so it is not enough
//	      here: a deployed pod that lost the variable must never be able to
//	      sign anyone in with a code read from an outbox.
//	none  no delivery: OTP requests are refused with
//	      ERR_OTP_DELIVERY_UNAVAILABLE, as identity has always done without a
//	      provider.
//
// Unset, it is mock when ENVIRONMENT is explicitly development, and none
// otherwise, so staging and production behave exactly as before. An explicit
// mock in any other environment, an unknown value, or an outbox file without
// the mock, stops startup.
//
// Real delivery-only providers (msg91, twilio) are selected the same way:
// otp_providers.go. Using one needs the owner's approval of the SMS and DLT
// category (docs/phase19/M1-APPROVAL-REQUESTS.md, R1); nothing here supplies
// credentials.
// The variables are read only when identity is activated; they never activate
// it.

// OTP delivery variables.
const (
	OtpProviderVariable       = EnvPrefix + "_OTP_PROVIDER"
	OtpMockOutboxFileVariable = EnvPrefix + "_OTP_MOCK_OUTBOX_FILE"
	environmentVariable       = EnvPrefix + "_ENVIRONMENT"
)

// OTP provider names.
const (
	OtpProviderMock = "mock"
	OtpProviderNone = "none"
)

// OtpDelivery is the resolved OTP delivery configuration. The zero value is
// no delivery.
type OtpDelivery struct {
	// Provider is OtpProviderMock, the real provider list as configured (for
	// example "msg91,twilio"), or "" for no delivery.
	Provider string
	// MockOutboxFile is the mock's outbox file, or "".
	MockOutboxFile string
	// real holds the selected real providers, in failover order, and their
	// variables; nil for the mock or no delivery. A pointer keeps OtpDelivery
	// comparable.
	real *realProviders
}

// realProviders is the resolved real-provider selection.
type realProviders struct {
	names    []string
	settings map[string]string
}

// ResolveOtpDelivery applies the rules above to the process environment.
func ResolveOtpDelivery() (OtpDelivery, error) {
	return resolveOtpDelivery(os.LookupEnv)
}

func resolveOtpDelivery(lookup func(string) (string, bool)) (OtpDelivery, error) {
	env, envSet := lookup(environmentVariable)
	explicitDevelopment := envSet && env == "development"

	provider, providerSet := lookup(OtpProviderVariable)
	outbox, outboxSet := lookup(OtpMockOutboxFileVariable)
	for _, v := range []struct {
		name, value string
		set         bool
	}{{OtpProviderVariable, provider, providerSet}, {OtpMockOutboxFileVariable, outbox, outboxSet}} {
		if !v.set {
			continue
		}
		if strings.TrimSpace(v.value) == "" {
			return OtpDelivery{}, fmt.Errorf("%w: %s is blank", ErrInvalidConfig, v.name)
		}
		if strings.TrimSpace(v.value) != v.value {
			return OtpDelivery{}, fmt.Errorf("%w: %s has leading or trailing whitespace", ErrInvalidConfig, v.name)
		}
	}

	if !providerSet {
		provider = OtpProviderNone
		if explicitDevelopment {
			provider = OtpProviderMock
		}
	}
	switch provider {
	case OtpProviderMock:
		if !explicitDevelopment {
			return OtpDelivery{}, fmt.Errorf("%w: %s=%s is refused unless %s is set to development: the mock lets anyone who can read its outbox sign in",
				ErrInvalidConfig, OtpProviderVariable, OtpProviderMock, environmentVariable)
		}
	case OtpProviderNone:
	default:
		return resolveRealSelection(provider, outboxSet, lookup)
	}
	if _, err := resolveRealProviders(nil, lookup); err != nil {
		return OtpDelivery{}, err // a real provider's variable without that provider selected
	}
	if outboxSet && provider != OtpProviderMock {
		return OtpDelivery{}, fmt.Errorf("%w: %s is set, but the OTP provider is not the mock", ErrInvalidConfig, OtpMockOutboxFileVariable)
	}
	if provider == OtpProviderNone {
		return OtpDelivery{}, nil // the zero value is no delivery
	}
	return OtpDelivery{Provider: provider, MockOutboxFile: outbox}, nil
}

// otpSender builds the sender for d: nil, and no outbox, for no delivery.
func (d OtpDelivery) otpSender(observe func(string, otpdelivery.Outcome)) (*otpdelivery.Sender, *otpdelivery.Outbox, error) {
	if d.real != nil {
		sender, err := d.realSender(observe)
		return sender, nil, err
	}
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
