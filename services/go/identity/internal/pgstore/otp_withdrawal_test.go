package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// stillTrying is a provider chain still trying when the request ends: it
// returns only when its context does, as otpdelivery does on abandonment.
type stillTrying struct{}

func (stillTrying) SendOtp(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// When delivery ends because the request did (its deadline, or the client
// going away), the undelivered challenge is still deleted from PostgreSQL. A
// delete issued on the request's own, ended context never reaches the
// database: pgx refuses it first.
func TestPostgres_UndeliveredChallengeIsWithdrawnAfterTheRequestEnds(t *testing.T) {
	env := requirePostgres(t)
	for name, end := range map[string]func() (context.Context, context.CancelFunc){
		"deadline": func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 100*time.Millisecond)
		},
		"client gone": func() (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(context.Background())
			time.AfterFunc(100*time.Millisecond, cancel)
			return c, cancel
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := app.NewIdentityService(NewRepository(env.poolA), replicaSecret)
			svc.SetOtpSender(stillTrying{})
			reqCtx, cancel := end()
			defer cancel()
			if req, err := svc.RequestOtp(reqCtx, replicaMSISDN); !errors.Is(err, app.ErrOtpDeliveryFailed) || req != "" {
				t.Fatalf("(%q, %v), want ErrOtpDeliveryFailed and no request id", req, err)
			}
			var live int
			if err := env.poolB.QueryRow(ctx, `SELECT count(*) FROM identity_otp_challenges`).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if live != 0 {
				t.Errorf("%d undelivered challenge(s) still live in PostgreSQL", live)
			}
		})
	}
}
