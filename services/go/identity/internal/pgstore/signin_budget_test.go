package pgstore

import (
	"sync/atomic"
	"testing"
	"time"
)

// The global hourly sign-in delivery cap at the repository boundary
// (F-1, app/signin_controls.go). The contract case runs on both backends; on
// PostgreSQL its concurrent calls alternate between two pools, so they are
// cross-instance calls, as two identity replicas make them. The PostgreSQL
// tests below cover the migration and housekeeping.

func testSignInDeliveryBudget(t *testing.T, be backend) {
	hour := t0.Truncate(time.Hour)
	next := hour.Add(time.Hour)

	// Exactly the limit, concurrently: 20 calls, limit 7, 7 true.
	var within atomic.Int64
	race(20, func(i int) {
		ok, err := be.pick(i).ReserveSignInDelivery(ctx, hour, 7)
		if err != nil {
			t.Errorf("reserve %d: %v", i, err)
		}
		if ok {
			within.Add(1)
		}
	})
	if within.Load() != 7 {
		t.Fatalf("%d of 20 concurrent reservations within a limit of 7", within.Load())
	}

	// A refused call counted nothing: with the limit raised by one, exactly
	// one more fits.
	for i, want := range []bool{true, false} {
		if ok, err := be.b.ReserveSignInDelivery(ctx, hour, 8); err != nil || ok != want {
			t.Errorf("after the limit was raised, call %d: (%v, %v), want %v", i+1, ok, err, want)
		}
	}

	// Hours are independent, and the same instant in another zone is the same
	// hour.
	ist := time.FixedZone("IST", 5*3600+1800)
	for i, want := range []bool{true, true, false} {
		if ok, err := be.pick(i).ReserveSignInDelivery(ctx, next.In(ist), 2); err != nil || ok != want {
			t.Errorf("the next hour, call %d: (%v, %v), want %v", i+1, ok, err, want)
		}
	}
	if ok, _ := be.a.ReserveSignInDelivery(ctx, hour, 8); ok {
		t.Error("the next hour's calls changed this hour's count")
	}

	// A limit below 1 allows nothing and counts nothing.
	other := hour.Add(-time.Hour)
	for _, limit := range []int{0, -1} {
		if ok, err := be.a.ReserveSignInDelivery(ctx, other, limit); err != nil || ok {
			t.Errorf("limit %d: (%v, %v), want false", limit, ok, err)
		}
	}
	if ok, _ := be.a.ReserveSignInDelivery(ctx, other, 1); !ok {
		t.Error("a refused limit-0 call was counted")
	}
}

// The migration creates the table, refusing a negative count.
func TestSignInBudget_Postgres_Table(t *testing.T) {
	env := requirePostgres(t)
	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_signin_delivery_budget (hour_start, deliveries) VALUES ($1, -1)`,
		t0); err == nil {
		t.Error("a negative count was stored")
	}
	var columns []string
	rows, err := env.poolA.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'identity_signin_delivery_budget' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, c)
	}
	if len(columns) != 2 || columns[0] != "hour_start" || columns[1] != "deliveries" {
		t.Errorf("columns = %v: the budget holds an hour and a count, nothing else", columns)
	}
}

// Many concurrent reservations from many connections on both pools: the cap
// holds exactly.
func TestSignInBudget_Postgres_ExactUnderHeavyConcurrency(t *testing.T) {
	env := requirePostgres(t)
	a, b := NewRepository(env.poolA), NewRepository(env.poolB)
	hour := time.Now().UTC().Truncate(time.Hour)
	var within atomic.Int64
	race(60, func(i int) {
		repo := a
		if i%2 == 1 {
			repo = b
		}
		ok, err := repo.ReserveSignInDelivery(ctx, hour, 13)
		if err != nil {
			t.Errorf("reserve %d: %v", i, err)
		}
		if ok {
			within.Add(1)
		}
	})
	if within.Load() != 13 {
		t.Fatalf("%d of 60 within a limit of 13", within.Load())
	}
	var stored int
	if err := env.poolA.QueryRow(ctx, `SELECT deliveries FROM identity_signin_delivery_budget WHERE hour_start = $1`, hour).Scan(&stored); err != nil || stored != 13 {
		t.Errorf("stored count (%d, %v), want 13", stored, err)
	}
}

// Housekeeping deletes old hours of the sign-in budget and keeps the current
// one.
func TestSignInBudget_Postgres_Housekeeping(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	now := time.Now()
	current := now.UTC().Truncate(time.Hour)
	for _, h := range []time.Time{current.Add(-5 * time.Hour), current.Add(-3 * time.Hour), current} {
		if _, err := repo.ReserveSignInDelivery(ctx, h, 10); err != nil {
			t.Fatal(err)
		}
	}
	res, err := housekeeper(t, env.poolA, now, 100).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.SignInBudgetHours != 2 {
		t.Errorf("housekeeping deleted %d sign-in budget hours, want 2", res.SignInBudgetHours)
	}
	if res.Total() < res.SignInBudgetHours {
		t.Errorf("Total %d does not include the sign-in budget hours", res.Total())
	}
	if within, _ := repo.ReserveSignInDelivery(ctx, current, 1); within { // the current hour already holds 1
		t.Error("the current hour's count was deleted")
	}
}
