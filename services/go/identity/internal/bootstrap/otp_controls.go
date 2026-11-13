package bootstrap

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
)

// # Sign-in delivery controls (F-1; ADR-0033 §7, approved for M1 2026-11-12)
//
// Two variables bound what a real SMS provider can cost
// (app/signin_controls.go):
//
//	CS_IDENTITY_OTP_ALLOWED_NUMBERS_FILE  the absolute path of a mounted file
//	                                      (a Secret: tester numbers are
//	                                      personal data) listing the approved
//	                                      M1 tester numbers, one per line, as
//	                                      +91 and ten digits, the first 6-9.
//	                                      Blank lines and lines starting with
//	                                      # are ignored; surrounding spaces are
//	                                      removed. At least one number; none
//	                                      twice. Only these numbers are sent a
//	                                      code.
//	CS_IDENTITY_OTP_SIGNIN_HOURLY_CAP     a decimal integer from 1 to 10000:
//	                                      the most sign-in codes handed to the
//	                                      provider in one clock hour, across
//	                                      every number and every replica.
//
// Rules, all checked at startup:
//   - A REAL provider (msg91, twilio) REQUIRES BOTH. Either missing, blank, an
//     unreadable or empty file, a line that is not an Indian mobile number in
//     canonical form, a duplicate, or a cap that is not a decimal integer in
//     range stops startup (ErrInvalidConfig).
//   - With the development mock they are OPTIONAL, to exercise the controls;
//     if either is set, both are required and checked as above.
//   - With no delivery, setting either stops startup: stray configuration.
//
// Errors name the variable and, for the file, a line number: never a number
// from the file, its path, or the cap's value. The file is read once, here;
// the service keeps only the numbers' pseudonyms (HMAC under the identity
// key), and Open installs the controls BEFORE the sender, in
// installOtpDelivery, the only place a sender is installed.

// Sign-in delivery control variables.
const (
	OtpAllowedNumbersFileVariable = EnvPrefix + "_OTP_ALLOWED_NUMBERS_FILE"
	OtpSignInHourlyCapVariable    = EnvPrefix + "_OTP_SIGNIN_HOURLY_CAP"
)

// MaxAllowedNumbersFileBytes bounds the allowlist file: thousands of lines,
// far above any tester list.
const MaxAllowedNumbersFileBytes = 64 << 10

// signInControls is the resolved allowlist (raw canonical numbers, read from
// the file) and hourly cap. A pointer in OtpDelivery keeps that comparable.
type signInControls struct {
	allowed   []string
	hourlyCap int
}

// resolveSignInControls applies the rules above: the controls are required
// for a real provider and not permitted with no delivery. Nil means none are
// configured.
func resolveSignInControls(required, permitted bool, lookup func(string) (string, bool)) (*signInControls, error) {
	file, fileSet := lookup(OtpAllowedNumbersFileVariable)
	capValue, capSet := lookup(OtpSignInHourlyCapVariable)
	if !permitted {
		for _, v := range []struct {
			name string
			set  bool
		}{{OtpAllowedNumbersFileVariable, fileSet}, {OtpSignInHourlyCapVariable, capSet}} {
			if v.set {
				return nil, fmt.Errorf("%w: %s is set, but no OTP provider is configured", ErrInvalidConfig, v.name)
			}
		}
		return nil, nil
	}
	if !fileSet && !capSet && !required {
		return nil, nil
	}
	for _, v := range []struct {
		name string
		set  bool
	}{{OtpAllowedNumbersFileVariable, fileSet}, {OtpSignInHourlyCapVariable, capSet}} {
		if !v.set {
			why := "with the other sign-in delivery control"
			if required {
				why = "when a real OTP provider is configured"
			}
			return nil, fmt.Errorf("%w: %s is required %s", ErrInvalidConfig, v.name, why)
		}
	}
	hourlyCap, err := parseSignInHourlyCap(capValue)
	if err != nil {
		return nil, err
	}
	allowed, err := readAllowedNumbersFile(file)
	if err != nil {
		return nil, err
	}
	return &signInControls{allowed: allowed, hourlyCap: hourlyCap}, nil
}

// parseSignInHourlyCap reads a decimal integer in [1, app.MaxSignInHourlyCap]:
// ASCII digits only, no sign, no spaces. The value is not echoed.
func parseSignInHourlyCap(value string) (int, error) {
	bad := fmt.Errorf("%w: %s must be a decimal integer from 1 to %d", ErrInvalidConfig, OtpSignInHourlyCapVariable, app.MaxSignInHourlyCap)
	if value == "" || len(value) > 5 {
		return 0, bad
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, bad
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > app.MaxSignInHourlyCap {
		return 0, bad
	}
	return n, nil
}

// readAllowedNumbersFile reads and checks the allowlist file. Errors name the
// variable and a line number, never a number, the path or file content.
func readAllowedNumbersFile(path string) ([]string, error) {
	name := OtpAllowedNumbersFileVariable
	switch {
	case strings.TrimSpace(path) == "":
		return nil, fmt.Errorf("%w: %s is blank", ErrInvalidConfig, name)
	case strings.TrimSpace(path) != path:
		return nil, fmt.Errorf("%w: %s has leading or trailing whitespace", ErrInvalidConfig, name)
	case !filepath.IsAbs(path):
		return nil, fmt.Errorf("%w: %s must be an absolute path", ErrInvalidConfig, name)
	}
	data, err := readSecretFile(name, path, MaxAllowedNumbersFileBytes)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	var allowed []string
	firstLine := map[string]int{}
	for i, raw := range bytes.Split(data, []byte("\n")) {
		line := string(bytes.TrimSpace(raw))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !isIndianMobile(line) {
			return nil, fmt.Errorf("%w: %s: line %d is not an Indian mobile number in canonical form (+91 and ten digits, the first 6-9)", ErrInvalidConfig, name, i+1)
		}
		if at, dup := firstLine[line]; dup {
			return nil, fmt.Errorf("%w: %s: lines %d and %d list the same number", ErrInvalidConfig, name, at, i+1)
		}
		firstLine[line] = i + 1
		allowed = append(allowed, line)
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%w: %s: the file lists no number", ErrInvalidConfig, name)
	}
	return allowed, nil
}

// isIndianMobile is the adapters' domestic rule (otpdelivery/domestic.go) on
// identity's canonical spelling: "+91" and ten ASCII digits, the first 6-9.
func isIndianMobile(s string) bool {
	national, ok := strings.CutPrefix(s, "+91")
	if !ok || len(national) != 10 || national[0] < '6' || national[0] > '9' {
		return false
	}
	for i := 0; i < len(national); i++ {
		if national[i] < '0' || national[i] > '9' {
			return false
		}
	}
	return true
}

// installOtpDelivery builds d's sender and installs it on svc, with the
// sign-in delivery controls FIRST. It is the only place in this package that
// installs a sender, and it refuses a real provider without the controls,
// whatever produced d: a real sender can never serve without them. It returns
// the mock's outbox, or nil.
func installOtpDelivery(svc *app.IdentityService, d OtpDelivery, obs *deliveryObserver) (*otpdelivery.Outbox, error) {
	if d.real != nil && d.controls == nil {
		return nil, fmt.Errorf("%w: a real OTP provider requires %s and %s", ErrInvalidConfig,
			OtpAllowedNumbersFileVariable, OtpSignInHourlyCapVariable)
	}
	sender, outbox, err := d.otpSender(obs)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		if d.controls != nil {
			return nil, fmt.Errorf("%w: sign-in delivery controls are set, but no OTP provider is configured", ErrInvalidConfig)
		}
		return nil, nil
	}
	if d.controls != nil {
		// The app's errors name positions only, never a number.
		if err := svc.SetSignInDeliveryControls(d.controls.allowed, d.controls.hourlyCap); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
	}
	svc.SetOtpSender(sender)
	return outbox, nil
}
