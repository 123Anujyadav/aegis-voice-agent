package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Real PostgreSQL (skipped without AEGIS_IDENTITY_PG_DSN, like the other
// Open tests): the F-1 hourly cap installed by Open holds across two runtimes
// on one database, as two identity replicas would share it. The development
// mock delivers, so nothing leaves the process.
func TestOpen_Postgres_SignInCapHoldsAcrossRuntimes(t *testing.T) {
	dsn := pgSchemaDSN(t)
	const hourlyCap = 4
	numbers := make([]string, 12)
	for i := range numbers {
		numbers[i] = fmt.Sprintf("+9190000001%02d", i)
	}
	env := map[string]string{
		environmentVariable:           "development",
		OtpProviderVariable:           "mock",
		OtpAllowedNumbersFileVariable: allowlistFile(t, strings.Join(numbers, "\n")+"\n"),
		OtpSignInHourlyCapVariable:    fmt.Sprint(hourlyCap),
	}
	d, err := resolveOtpDelivery(lookupFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := fullConfig(t, dsn)
	c.dbTLS = pgshared.AllowPlaintextForDevelopment // the disposable test database is plaintext
	c.otp = d
	// One fixed instant in the middle of an hour, so the test cannot straddle
	// an hour boundary.
	clock := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	now := func() time.Time { return clock }
	var runtimes []*Runtime
	for range 2 {
		rt, openErr := Open(context.Background(), c, now)
		if openErr != nil {
			t.Fatalf("Open: %v", openErr)
		}
		t.Cleanup(rt.Close)
		if n, gotCap, ok := rt.Service.SignInDeliveryControls(); !ok || n != len(numbers) || gotCap != hourlyCap {
			t.Fatalf("controls (%d, %d, %v)", n, gotCap, ok)
		}
		runtimes = append(runtimes, rt)
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		refused int
		other   []error
	)
	for i, n := range numbers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reqErr := runtimes[i%2].Service.RequestOtp(context.Background(), n)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case reqErr == nil:
				ok++
			case errors.Is(reqErr, app.ErrOtpDeliveryUnavailable):
				refused++
			default:
				other = append(other, reqErr)
			}
		}()
	}
	wg.Wait()
	if ok != hourlyCap || refused != len(numbers)-hourlyCap || len(other) != 0 {
		t.Fatalf("%d sent, %d refused, other %v; want %d and %d", ok, refused, other, hourlyCap, len(numbers)-hourlyCap)
	}
	if sent := runtimes[0].OtpOutbox().Len() + runtimes[1].OtpOutbox().Len(); sent != hourlyCap {
		t.Errorf("%d codes delivered across both runtimes, want %d", sent, hourlyCap)
	}
	// A number off the list is refused by either runtime.
	if _, err := runtimes[1].Service.RequestOtp(context.Background(), "+919999999999"); !errors.Is(err, app.ErrOtpDeliveryUnavailable) {
		t.Errorf("a number off the list: %v", err)
	}
}
