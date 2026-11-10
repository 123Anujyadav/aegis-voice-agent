package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

// The mock is the default only where ENVIRONMENT says development explicitly;
// everywhere else the default is no delivery, exactly as before.
func TestResolveOtpDelivery_Defaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit development selects the mock", map[string]string{environmentVariable: "development"}, OtpProviderMock},
		{"unset environment selects none", map[string]string{}, OtpProviderNone},
		{"staging selects none", map[string]string{environmentVariable: "staging"}, OtpProviderNone},
		{"production selects none", map[string]string{environmentVariable: "production"}, OtpProviderNone},
		{"explicit none in development", map[string]string{environmentVariable: "development", OtpProviderVariable: "none"}, OtpProviderNone},
		{"explicit none in production", map[string]string{environmentVariable: "production", OtpProviderVariable: "none"}, OtpProviderNone},
		{"explicit mock in development", map[string]string{environmentVariable: "development", OtpProviderVariable: "mock"}, OtpProviderMock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOtpDelivery(lookupFrom(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			provider := got.Provider
			if provider == "" {
				provider = OtpProviderNone
			}
			if provider != tc.want {
				t.Fatalf("provider %q, want %q", got.Provider, tc.want)
			}
		})
	}
}

// The mock lets anyone who reads its outbox sign in: it is refused in every
// environment that is not explicitly development, including an unset one.
func TestResolveOtpDelivery_MockIsRefusedOutsideExplicitDevelopment(t *testing.T) {
	for _, env := range []map[string]string{
		{OtpProviderVariable: "mock"},
		{OtpProviderVariable: "mock", environmentVariable: "staging"},
		{OtpProviderVariable: "mock", environmentVariable: "production"},
		{OtpProviderVariable: "mock", environmentVariable: "Development"},
		{OtpProviderVariable: "mock", environmentVariable: "production", OtpMockOutboxFileVariable: "/tmp/otp"},
	} {
		_, err := resolveOtpDelivery(lookupFrom(env))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "refused") {
			t.Errorf("%v: err %v, want the mock refused", env, err)
		}
	}
}

func TestResolveOtpDelivery_RefusesInvalidValues(t *testing.T) {
	dev := environmentVariable
	for name, env := range map[string]map[string]string{
		"unknown provider":         {dev: "development", OtpProviderVariable: "sms-gateway"},
		"provider in capitals":     {dev: "development", OtpProviderVariable: "MOCK"},
		"blank provider":           {dev: "development", OtpProviderVariable: "  "},
		"padded provider":          {dev: "development", OtpProviderVariable: " mock"},
		"blank outbox":             {dev: "development", OtpMockOutboxFileVariable: ""},
		"outbox without the mock":  {dev: "development", OtpProviderVariable: "none", OtpMockOutboxFileVariable: "/tmp/otp"},
		"outbox in production":     {dev: "production", OtpMockOutboxFileVariable: "/tmp/otp"},
		"padded outbox":            {dev: "development", OtpMockOutboxFileVariable: "/tmp/otp "},
		"credential-looking value": {dev: "development", OtpProviderVariable: "AC0123456789abcdef"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveOtpDelivery(lookupFrom(env))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err %v, want ErrInvalidConfig", err)
			}
			// An unknown value is never echoed: it may be a misplaced secret.
			if v := env[OtpProviderVariable]; strings.TrimSpace(v) != "" && strings.Contains(err.Error(), strings.TrimSpace(v)) && v != "mock" && v != "none" {
				t.Errorf("the error echoes the value: %v", err)
			}
		})
	}
}

func TestResolveOtpDelivery_MockWithOutboxFile(t *testing.T) {
	got, err := resolveOtpDelivery(lookupFrom(map[string]string{
		environmentVariable: "development", OtpMockOutboxFileVariable: "/tmp/otp-outbox.txt",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != OtpProviderMock || got.MockOutboxFile != "/tmp/otp-outbox.txt" {
		t.Fatalf("%+v", got)
	}
}

// Only the mock builds a sender; none, and the zero value, build nothing.
func TestOtpDelivery_Sender(t *testing.T) {
	for _, d := range []OtpDelivery{{}, {Provider: OtpProviderNone}} {
		s, o, err := d.otpSender(nil)
		if err != nil || s != nil || o != nil {
			t.Fatalf("%+v: sender %v outbox %v err %v, want nothing", d, s, o, err)
		}
	}
	s, o, err := OtpDelivery{Provider: OtpProviderMock}.otpSender(nil)
	if err != nil || s == nil || o == nil {
		t.Fatalf("mock: sender %v outbox %v err %v", s, o, err)
	}
	if h := s.Health(); len(h) != 1 || h[0].Name != "mock" {
		t.Fatalf("health %+v", h)
	}
	if _, ok := s.DeletionCodes(); ok {
		t.Fatal("the mock must not deliver account-deletion codes")
	}
}

// The OTP variables never activate identity on their own.
func TestOtpVariablesDoNotActivateIdentity(t *testing.T) {
	for _, name := range ActivationVariables {
		if name == OtpProviderVariable || name == OtpMockOutboxFileVariable {
			t.Fatalf("%s is an activation variable", name)
		}
	}
}

// Through LoadActivation, with every activation variable set: the OTP
// delivery is resolved with the rest, and an invalid choice stops activation.
func TestLoadActivation_ResolvesOtpDelivery(t *testing.T) {
	c, _, _ := fullConfig(t, "postgres://u@127.0.0.1:1/db")
	t.Run("development defaults to the mock", func(t *testing.T) {
		setActivationEnv(t, c)
		t.Setenv(environmentVariable, "development")
		unset(t, OtpProviderVariable)
		got, active, err := LoadActivation()
		if !active || err != nil || got.otp.Provider != OtpProviderMock {
			t.Fatalf("(%+v, %v, %v), want the mock", got.otp, active, err)
		}
	})
	t.Run("production refuses the mock", func(t *testing.T) {
		setActivationEnv(t, c)
		t.Setenv(environmentVariable, "production")
		t.Setenv(OtpProviderVariable, OtpProviderMock)
		_, active, err := LoadActivation()
		if !active {
			t.Fatal("not active")
		}
		wantInvalid(t, err, OtpProviderVariable)
	})
	t.Run("production defaults to none", func(t *testing.T) {
		setActivationEnv(t, c)
		t.Setenv(environmentVariable, "production")
		unset(t, OtpProviderVariable)
		got, _, err := LoadActivation()
		if err != nil || got.otp != (OtpDelivery{}) {
			t.Fatalf("(%+v, %v), want no delivery", got.otp, err)
		}
	})
}
