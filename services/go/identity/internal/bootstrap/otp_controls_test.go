package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// The F-1 sign-in delivery controls at startup (otp_controls.go): required
// with a real provider, optional with the mock, refused with no delivery;
// every error is ErrInvalidConfig and none carries a number from the file.
// Nothing here sends a message or contacts a provider.

// Test tester numbers: Indian mobile shape, test-only values.
var testerNumbers = []string{"+919000000001", "+919000000002", "+918000000003"}

func allowlistFile(t *testing.T, content string) string {
	t.Helper()
	return writeFile(t, "allowed-numbers", []byte(content))
}

// controlsEnv is a valid pair of controls: the three tester numbers, cap 7.
func controlsEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		OtpAllowedNumbersFileVariable: allowlistFile(t, "# M1 testers\n"+strings.Join(testerNumbers, "\n")+"\n"),
		OtpSignInHourlyCapVariable:    "7",
	}
}

// assertNoNumber fails if err carries any digit run from the numbers, or the
// path of the file.
func assertNoNumber(t *testing.T, err error, extra ...string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, n := range append(append([]string{}, testerNumbers...), extra...) {
		if n == "" {
			continue
		}
		national := strings.TrimPrefix(n, "+91")
		if strings.Contains(err.Error(), n) || strings.Contains(err.Error(), national) {
			t.Errorf("the error carries %q: %v", n, err)
		}
	}
}

func realEnv(t *testing.T, controls map[string]string) map[string]string {
	t.Helper()
	return merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: "msg91"}, msg91Env(t), controls)
}

func TestSignInControls_RealProviderRequiresBothValid(t *testing.T) {
	valid := controlsEnv(t)
	file := valid[OtpAllowedNumbersFileVariable]
	withFile := func(content string) map[string]string {
		return map[string]string{OtpAllowedNumbersFileVariable: allowlistFile(t, content), OtpSignInHourlyCapVariable: "7"}
	}
	withCap := func(c string) map[string]string {
		return map[string]string{OtpAllowedNumbersFileVariable: file, OtpSignInHourlyCapVariable: c}
	}
	odd := []string{
		"+91 9000000004", "919000000004", "+9190000000045", "+91900000000", "+915000000004", "+14155552671",
		"+91900000000x", "9000000004", "+91-9000000004", "+910000000004", "＋919000000004", "+919000000004 # Asha",
	}
	type refusal struct {
		controls map[string]string
		mention  string
	}
	cases := map[string]refusal{
		"neither":             {map[string]string{}, OtpAllowedNumbersFileVariable},
		"no allowlist file":   {map[string]string{OtpSignInHourlyCapVariable: "7"}, OtpAllowedNumbersFileVariable},
		"no cap":              {map[string]string{OtpAllowedNumbersFileVariable: file}, OtpSignInHourlyCapVariable},
		"blank file variable": {map[string]string{OtpAllowedNumbersFileVariable: " ", OtpSignInHourlyCapVariable: "7"}, "blank"},
		"padded file path":    {map[string]string{OtpAllowedNumbersFileVariable: file + " ", OtpSignInHourlyCapVariable: "7"}, "whitespace"},
		"missing file":        {map[string]string{OtpAllowedNumbersFileVariable: filepath.Join(t.TempDir(), "absent"), OtpSignInHourlyCapVariable: "7"}, "does not exist"},
		"relative path":       {map[string]string{OtpAllowedNumbersFileVariable: "allowed-numbers", OtpSignInHourlyCapVariable: "7"}, "absolute"},
		"a directory":         {map[string]string{OtpAllowedNumbersFileVariable: t.TempDir(), OtpSignInHourlyCapVariable: "7"}, "regular file"},
		"empty file":          {withFile(""), "lists no number"},
		"only comments":       {withFile("# testers\n\n   \n# none yet\n"), "lists no number"},
		"duplicates":          {withFile(strings.Join(testerNumbers, "\n") + "\n" + testerNumbers[1] + "\n"), "lines 2 and 4"},
		"duplicate, padded":   {withFile(testerNumbers[0] + "\n  " + testerNumbers[0] + "  \n"), "lines 1 and 2"},
		"cap 0":               {withCap("0"), OtpSignInHourlyCapVariable},
		"cap -1":              {withCap("-1"), OtpSignInHourlyCapVariable},
		"cap abc":             {withCap("abc"), OtpSignInHourlyCapVariable},
		"cap 10001":           {withCap("10001"), OtpSignInHourlyCapVariable},
		"cap blank":           {withCap(""), OtpSignInHourlyCapVariable},
		"cap padded":          {withCap(" 7"), OtpSignInHourlyCapVariable},
		"cap signed":          {withCap("+7"), OtpSignInHourlyCapVariable},
		"cap decimal":         {withCap("7.0"), OtpSignInHourlyCapVariable},
		"cap huge":            {withCap("99999999999999999999"), OtpSignInHourlyCapVariable},
	}
	for _, n := range odd {
		cases["line "+n] = refusal{withFile(testerNumbers[0] + "\n# comment\n" + n + "\n" + testerNumbers[1]), "line 3 is not"}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := realEnv(t, tc.controls)
			d, err := resolveOtpDelivery(lookupFrom(env))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("(%+v, %v), want ErrInvalidConfig", d, err)
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("the error does not say %q: %v", tc.mention, err)
			}
			assertNoNumber(t, err, odd...)
			if p := tc.controls[OtpAllowedNumbersFileVariable]; filepath.IsAbs(p) && strings.Contains(err.Error(), p) {
				t.Errorf("the error carries the file's path: %v", err)
			}
		})
	}
	// Twilio and both providers are held to the same rule.
	for _, provider := range []string{"twilio", "msg91,twilio"} {
		env := merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: provider}, msg91Env(t), twilioEnv(t))
		if provider == "twilio" {
			env = merge(map[string]string{environmentVariable: "staging", OtpProviderVariable: provider}, twilioEnv(t))
		}
		if _, err := resolveOtpDelivery(lookupFrom(env)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s without the controls: %v, want ErrInvalidConfig", provider, err)
		}
	}
}

func TestSignInControls_FileFormat(t *testing.T) {
	content := "# M1 tester numbers (personal data: a mounted Secret)\r\n\r\n  " + testerNumbers[0] + "  \r\n" +
		"\t# a comment\n" + testerNumbers[1] + "\n\n" + testerNumbers[2]
	env := realEnv(t, map[string]string{OtpAllowedNumbersFileVariable: allowlistFile(t, content), OtpSignInHourlyCapVariable: "10000"})
	d, err := resolveOtpDelivery(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if d.controls == nil || strings.Join(d.controls.allowed, ",") != strings.Join(testerNumbers, ",") || d.controls.hourlyCap != 10000 {
		t.Fatalf("controls = %+v", d.controls)
	}
	for _, c := range []string{"1", "05", "10000"} {
		if _, err := parseSignInHourlyCap(c); err != nil {
			t.Errorf("cap %q: %v", c, err)
		}
	}
}

func TestSignInControls_OptionalWithTheMock(t *testing.T) {
	dev := map[string]string{environmentVariable: "development", OtpProviderVariable: "mock"}
	d, err := resolveOtpDelivery(lookupFrom(dev))
	if err != nil || d.controls != nil {
		t.Fatalf("mock without controls: (%+v, %v)", d, err)
	}
	d, err = resolveOtpDelivery(lookupFrom(merge(dev, controlsEnv(t))))
	if err != nil || d.controls == nil || len(d.controls.allowed) != 3 || d.controls.hourlyCap != 7 {
		t.Fatalf("mock with controls: (%+v, %v)", d.controls, err)
	}
	// The default mock (development, provider unset) accepts them too.
	if d, err := resolveOtpDelivery(lookupFrom(merge(map[string]string{environmentVariable: "development"}, controlsEnv(t)))); err != nil || d.controls == nil {
		t.Fatalf("default mock with controls: (%+v, %v)", d, err)
	}
	// With the mock, one without the other is refused, and bad values are.
	only := controlsEnv(t)
	delete(only, OtpSignInHourlyCapVariable)
	for name, env := range map[string]map[string]string{
		"file without cap": merge(dev, only),
		"cap without file": merge(dev, map[string]string{OtpSignInHourlyCapVariable: "7"}),
		"bad cap":          merge(dev, controlsEnv(t), map[string]string{OtpSignInHourlyCapVariable: "0"}),
		"bad line":         merge(dev, map[string]string{OtpAllowedNumbersFileVariable: allowlistFile(t, "+14155552671\n"), OtpSignInHourlyCapVariable: "7"}),
	} {
		_, err := resolveOtpDelivery(lookupFrom(env))
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
		}
		assertNoNumber(t, err, "+14155552671")
	}
}

func TestSignInControls_RefusedWithNoDelivery(t *testing.T) {
	for name, base := range map[string]map[string]string{
		"staging default":      {environmentVariable: "staging"},
		"unset environment":    {},
		"explicit none":        {environmentVariable: "production", OtpProviderVariable: "none"},
		"none in development":  {environmentVariable: "development", OtpProviderVariable: "none"},
		"staging explicit one": {environmentVariable: "staging", OtpProviderVariable: "none"},
	} {
		for _, v := range []string{OtpAllowedNumbersFileVariable, OtpSignInHourlyCapVariable} {
			env := merge(base, map[string]string{v: controlsEnv(t)[v]})
			_, err := resolveOtpDelivery(lookupFrom(env))
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "no OTP provider") {
				t.Errorf("%s with %s: %v, want stray configuration refused", name, v, err)
			}
		}
	}
}

func TestInstallOtpDelivery_RealProviderInstallsTheControlsFirst(t *testing.T) {
	d, err := resolveOtpDelivery(lookupFrom(realEnv(t, controlsEnv(t))))
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewIdentityService(app.NewMemoryIdentityRepository(), []byte("0123456789abcdef0123456789abcdef"))
	outbox, err := installOtpDelivery(svc, d, nil)
	if err != nil || outbox != nil {
		t.Fatalf("(%v, %v)", outbox, err)
	}
	n, hourlyCap, ok := svc.SignInDeliveryControls()
	if !ok || n != len(testerNumbers) || hourlyCap != 7 || !svc.CanDeliverOtp() {
		t.Fatalf("controls (%d, %d, %v), can deliver %v", n, hourlyCap, ok, svc.CanDeliverOtp())
	}
	// Behaviour: a number not on the list is refused before anything else,
	// so no provider is contacted. (No allowlisted request is made here: it
	// would be handed to the real adapter.)
	if _, err := svc.RequestOtp(context.Background(), "+919999999999"); !errors.Is(err, app.ErrOtpDeliveryUnavailable) {
		t.Errorf("a number off the list: %v, want ErrOtpDeliveryUnavailable", err)
	}
}

// Defence in depth: however an OtpDelivery with a real provider comes to lack
// the controls, no sender is built or installed.
func TestInstallOtpDelivery_RefusesARealProviderWithoutTheControls(t *testing.T) {
	d, err := resolveOtpDelivery(lookupFrom(realEnv(t, controlsEnv(t))))
	if err != nil {
		t.Fatal(err)
	}
	d.controls = nil
	svc := app.NewIdentityService(app.NewMemoryIdentityRepository(), []byte("0123456789abcdef0123456789abcdef"))
	if _, err := installOtpDelivery(svc, d, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("installed: %v, want ErrInvalidConfig", err)
	}
	if svc.CanDeliverOtp() {
		t.Fatal("a real sender was installed without the controls")
	}
	if _, _, ok := svc.SignInDeliveryControls(); ok {
		t.Error("controls configured from nothing")
	}
	if sender, _, err := d.otpSender(nil); sender != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("otpSender built a real sender without the controls: (%v, %v)", sender, err)
	}
	// Controls without any provider are refused too.
	if _, err := installOtpDelivery(svc, OtpDelivery{controls: &signInControls{allowed: testerNumbers, hourlyCap: 1}}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("controls with no delivery: %v", err)
	}
}

func TestInstallOtpDelivery_MockWithAndWithoutTheControls(t *testing.T) {
	dev := map[string]string{environmentVariable: "development", OtpProviderVariable: "mock"}
	for name, env := range map[string]map[string]string{"without": dev, "with": merge(dev, controlsEnv(t))} {
		d, err := resolveOtpDelivery(lookupFrom(env))
		if err != nil {
			t.Fatal(err)
		}
		svc := app.NewIdentityService(app.NewMemoryIdentityRepository(), []byte("0123456789abcdef0123456789abcdef"))
		outbox, err := installOtpDelivery(svc, d, nil)
		if err != nil || outbox == nil || !svc.CanDeliverOtp() {
			t.Fatalf("%s: (%v, %v)", name, outbox, err)
		}
		_, _, configured := svc.SignInDeliveryControls()
		if configured != (name == "with") {
			t.Errorf("%s: controls configured = %v", name, configured)
		}
		_, err = svc.RequestOtp(context.Background(), "+919999999999")
		if name == "with" && !errors.Is(err, app.ErrOtpDeliveryUnavailable) {
			t.Errorf("with: a number off the list: %v", err)
		}
		if name == "without" && err != nil {
			t.Errorf("without: %v", err)
		}
		if _, err := svc.RequestOtp(context.Background(), testerNumbers[0]); err != nil {
			t.Errorf("%s: a tester: %v", name, err)
		}
		wantSent := 2
		if name == "with" {
			wantSent = 1
		}
		if outbox.Len() != wantSent {
			t.Errorf("%s: %d codes in the outbox, want %d", name, outbox.Len(), wantSent)
		}
	}
}

// installOtpDelivery is the only place in this package's code that installs
// an OTP sender, so no path can install a real one without the controls.
func TestInstallOtpDelivery_IsTheOnlySenderInstallation(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "SetOtpSender" {
					sites = append(sites, e.Name()+":"+fn.Name.Name)
				}
				return true
			})
		}
	}
	if strings.Join(sites, ",") != "otp_controls.go:installOtpDelivery" {
		t.Errorf("SetOtpSender is called from %v; want only otp_controls.go:installOtpDelivery", sites)
	}
}
