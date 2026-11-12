package bootstrap

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
)

// # Real OTP delivery (ADR-0033: delivery-only providers)
//
// CS_IDENTITY_OTP_PROVIDER may also name real providers, comma-separated, in
// the order they are tried: "msg91", "twilio", "msg91,twilio" or
// "twilio,msg91". Each selected provider needs every one of its variables
// below; a variable for a provider that is not selected stops startup, so a
// half-configured provider can never be used by accident. Credentials are
// FILES (mounted Secrets), never values.
//
// What this does not do:
//   - Activate anything: without these variables nothing changes, and the
//     staging overlay sets CS_IDENTITY_OTP_PROVIDER=none. Using a provider
//     needs the owner's R1 approval (account, DLT entity, header and
//     templates, India domestic route, billing).
//   - Wire account-deletion codes: the adapters can send them, but enabling
//     web deletion codes is decision O13; Sender.DeletionCodes is not
//     installed here.

// Real provider names.
const (
	OtpProviderMSG91  = "msg91"
	OtpProviderTwilio = "twilio"
)

// Real provider variables.
const (
	OtpMSG91AuthKeyFileVariable  = EnvPrefix + "_OTP_MSG91_AUTH_KEY_FILE"
	OtpMSG91SignInFlowIDVariable = EnvPrefix + "_OTP_MSG91_SIGNIN_FLOW_ID"
	OtpMSG91CodeVariableVariable = EnvPrefix + "_OTP_MSG91_CODE_VARIABLE"

	OtpTwilioAccountSIDFileVariable    = EnvPrefix + "_OTP_TWILIO_ACCOUNT_SID_FILE"
	OtpTwilioAPIKeySIDFileVariable     = EnvPrefix + "_OTP_TWILIO_API_KEY_SID_FILE"
	OtpTwilioAPIKeySecretFileVariable  = EnvPrefix + "_OTP_TWILIO_API_KEY_SECRET_FILE"
	OtpTwilioMessagingServiceVariable  = EnvPrefix + "_OTP_TWILIO_MESSAGING_SERVICE_SID"
	OtpTwilioSignInTemplateVariable    = EnvPrefix + "_OTP_TWILIO_SIGNIN_TEMPLATE"
	otpAttemptTimeout                  = 2 * time.Second
	maxRealOtpProviders                = 2
	otpProviderListSeparator           = ","
	otpRealProviderListExampleForError = "msg91, twilio, or both comma-separated"
)

// realProviderVariables lists, per provider, the variables it requires.
var realProviderVariables = map[string][]string{
	OtpProviderMSG91: {OtpMSG91AuthKeyFileVariable, OtpMSG91SignInFlowIDVariable, OtpMSG91CodeVariableVariable},
	OtpProviderTwilio: {
		OtpTwilioAccountSIDFileVariable, OtpTwilioAPIKeySIDFileVariable, OtpTwilioAPIKeySecretFileVariable,
		OtpTwilioMessagingServiceVariable, OtpTwilioSignInTemplateVariable,
	},
}

// parseRealProviders reads a provider list of one or two distinct real
// providers. It reports false for anything else, including mock and none.
func parseRealProviders(value string) (names []string, ok bool) {
	parts := strings.Split(value, otpProviderListSeparator)
	if len(parts) == 0 || len(parts) > maxRealOtpProviders {
		return nil, false
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if _, known := realProviderVariables[p]; !known || seen[p] {
			return nil, false
		}
		seen[p] = true
		names = append(names, p)
	}
	return names, true
}

// resolveRealSelection resolves a CS_IDENTITY_OTP_PROVIDER value that is
// neither mock nor none: a real provider list, complete, with no mock outbox.
func resolveRealSelection(provider string, outboxSet bool, lookup func(string) (string, bool)) (OtpDelivery, error) {
	names, ok := parseRealProviders(provider)
	if !ok {
		// The value is an operator's word, never a secret, but it is not
		// echoed: an unknown value may be a misplaced credential.
		return OtpDelivery{}, fmt.Errorf("%w: %s must be %s, %s, or %s", ErrInvalidConfig,
			OtpProviderVariable, OtpProviderMock, OtpProviderNone, otpRealProviderListExampleForError)
	}
	if outboxSet {
		return OtpDelivery{}, fmt.Errorf("%w: %s is set, but the OTP provider is not the mock", ErrInvalidConfig, OtpMockOutboxFileVariable)
	}
	settings, err := resolveRealProviders(names, lookup)
	if err != nil {
		return OtpDelivery{}, err
	}
	return OtpDelivery{Provider: provider, real: &realProviders{names: names, settings: settings}}, nil
}

// resolveRealProviders checks every provider variable against the selection
// and returns the selected providers' settings.
func resolveRealProviders(names []string, lookup func(string) (string, bool)) (map[string]string, error) {
	selected := map[string]bool{}
	for _, n := range names {
		selected[n] = true
	}
	settings := map[string]string{}
	for provider, vars := range realProviderVariables {
		for _, v := range vars {
			value, set := lookup(v)
			if !selected[provider] {
				if set {
					return nil, fmt.Errorf("%w: %s is set, but %s is not a selected OTP provider", ErrInvalidConfig, v, provider)
				}
				continue
			}
			if !set || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%w: %s is required when %s is a selected OTP provider", ErrInvalidConfig, v, provider)
			}
			settings[v] = value
		}
	}
	return settings, nil
}

// realSender builds the delivery-only adapters and their failover sender. It
// reads the credential files now, so a missing or malformed one stops startup.
func (d OtpDelivery) realSender(observe func(string, otpdelivery.Outcome)) (*otpdelivery.Sender, error) {
	providers := make([]otpdelivery.Provider, 0, len(d.real.names))
	settings := d.real.settings
	for _, name := range d.real.names {
		var (
			p   otpdelivery.Provider
			err error
		)
		switch name {
		case OtpProviderMSG91:
			p, err = otpdelivery.NewMSG91(otpdelivery.MSG91Config{
				Name:         OtpProviderMSG91,
				AuthKeyFile:  settings[OtpMSG91AuthKeyFileVariable],
				SignInFlowID: settings[OtpMSG91SignInFlowIDVariable],
				CodeVariable: settings[OtpMSG91CodeVariableVariable],
			})
		case OtpProviderTwilio:
			p, err = otpdelivery.NewTwilio(otpdelivery.TwilioConfig{
				Name:                OtpProviderTwilio,
				AccountSIDFile:      settings[OtpTwilioAccountSIDFileVariable],
				APIKeySIDFile:       settings[OtpTwilioAPIKeySIDFileVariable],
				APIKeySecretFile:    settings[OtpTwilioAPIKeySecretFileVariable],
				MessagingServiceSID: settings[OtpTwilioMessagingServiceVariable],
				SignInTemplate:      otpdelivery.Template(settings[OtpTwilioSignInTemplateVariable]),
			})
		}
		if err != nil {
			// The adapters' errors are fixed and carry no credential.
			return nil, fmt.Errorf("%w: OTP provider %s: %w", ErrInvalidConfig, name, err)
		}
		providers = append(providers, p)
	}
	sender, err := otpdelivery.New(otpdelivery.Config{
		Providers: providers, AttemptTimeout: otpAttemptTimeout, Observe: observe,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return sender, nil
}

// deliveryObserver forwards delivery outcomes to a hook installed after the
// runtime is built: the metrics registry is created later than the sender.
// Until a hook is installed, outcomes are dropped.
type deliveryObserver struct {
	mu   sync.RWMutex
	hook func(string, otpdelivery.Outcome)
}

func (o *deliveryObserver) observe(provider string, outcome otpdelivery.Outcome) {
	o.mu.RLock()
	hook := o.hook
	o.mu.RUnlock()
	if hook != nil {
		hook(provider, outcome)
	}
}

func (o *deliveryObserver) set(hook func(string, otpdelivery.Outcome)) {
	o.mu.Lock()
	o.hook = hook
	o.mu.Unlock()
}
