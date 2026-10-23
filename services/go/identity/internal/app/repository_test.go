package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// MemoryIdentityRepository returned its stored pointers from every Get*, so a
// caller could change persisted state without calling any Save* method.
//
// That is how the OTP attempt limit came to "work": ValidateOtp did
// challenge.Attempts++ and the increment landed in the map by aliasing, with
// nothing writing it back. IdentityRepository is an interface, and any
// implementation backed by a real store deserialises a fresh struct per read —
// so the identical service code would have discarded every increment and left a
// six-digit code open to unlimited guessing.
//
// These tests pin the contract that removes the ambiguity: reads hand back
// copies, and the only way to change stored state is to call Save*.

func ctx() context.Context { return context.Background() }

func storedOtp(t *testing.T, r *MemoryIdentityRepository, id string) *OtpChallenge {
	t.Helper()
	got, err := r.GetOtp(ctx(), id)
	if err != nil {
		t.Fatalf("GetOtp(%s): %v", id, err)
	}
	return got
}

// TestRepo_MutatingAGetOtpResultDoesNotChangeStoredState is the core invariant.
func TestRepo_MutatingAGetOtpResultDoesNotChangeStoredState(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	if err := r.SaveOtp(ctx(), &OtpChallenge{
		RequestID: "req_1", MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"), CodeHash: "hash", Attempts: 0,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveOtp: %v", err)
	}

	first := storedOtp(t, r, "req_1")
	first.Attempts = 99
	first.CodeHash = "tampered"

	again := storedOtp(t, r, "req_1")
	if again.Attempts != 0 {
		t.Errorf("Attempts = %d after mutating a returned value, want 0; the "+
			"repository handed out a pointer into its own state", again.Attempts)
	}
	if again.CodeHash != "hash" {
		t.Errorf("CodeHash = %q after mutating a returned value, want %q",
			again.CodeHash, "hash")
	}
}

// TestRepo_SaveOtpPersistsAnExplicitMutation is the other half: copying must not
// make writes impossible, only explicit.
func TestRepo_SaveOtpPersistsAnExplicitMutation(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	_ = r.SaveOtp(ctx(), &OtpChallenge{RequestID: "req_1", MSISDNPseudonym: mp("+919876543210"), Attempts: 0})

	got := storedOtp(t, r, "req_1")
	got.Attempts = 2
	if err := r.SaveOtp(ctx(), got); err != nil {
		t.Fatalf("SaveOtp: %v", err)
	}

	if again := storedOtp(t, r, "req_1"); again.Attempts != 2 {
		t.Errorf("Attempts = %d after an explicit SaveOtp, want 2", again.Attempts)
	}
}

// TestRepo_SaveOtpCopiesItsArgument closes the inbound half of the aliasing.
// Copying only on read would still let a caller keep a handle on stored state
// through the value it saved.
func TestRepo_SaveOtpCopiesItsArgument(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	mine := &OtpChallenge{RequestID: "req_1", MSISDNPseudonym: mp("+919876543210"), Attempts: 0}
	_ = r.SaveOtp(ctx(), mine)

	mine.Attempts = 42 // the caller keeps using its own struct

	if got := storedOtp(t, r, "req_1"); got.Attempts != 0 {
		t.Errorf("Attempts = %d; mutating the struct passed to SaveOtp changed "+
			"stored state, so the repository kept the caller's pointer", got.Attempts)
	}
}

// TestRepo_MutatingAGetSessionResultDoesNotChangeStoredState covers sessions,
// including the Roles SLICE — a struct copy alone shares its backing array, so
// roles[0] = "admin" would still reach persisted state.
func TestRepo_MutatingAGetSessionResultDoesNotChangeStoredState(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	_ = r.SaveSession(ctx(), &UserSession{
		SessionID: "sess_1", SubscriberID: "usr_1",
		RefreshTokenHash: rth("rt"), Roles: []string{"subscriber"},
	})

	got, err := r.GetSession(ctx(), "sess_1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	got.RefreshTokenHash = rth("stolen")
	got.IsRevoked = true
	got.Roles[0] = "admin"

	again, err := r.GetSession(ctx(), "sess_1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if again.RefreshTokenHash != rth("rt") {
		t.Errorf("RefreshTokenHash = %q, want the hash of %q", again.RefreshTokenHash, "rt")
	}
	if again.IsRevoked {
		t.Error("IsRevoked became true by mutating a returned session")
	}
	if again.Roles[0] != "subscriber" {
		t.Errorf("Roles[0] = %q, want %q — the copy shares the caller's backing "+
			"array, so a privilege string can be written into stored state",
			again.Roles[0], "subscriber")
	}
}

// TestRepo_SaveSessionCopiesItsRolesSlice is the inbound equivalent.
func TestRepo_SaveSessionCopiesItsRolesSlice(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	roles := []string{"subscriber"}
	_ = r.SaveSession(ctx(), &UserSession{SessionID: "sess_1", RefreshTokenHash: rth("rt"), Roles: roles})

	roles[0] = "admin" // the caller still owns this slice

	got, _ := r.GetSession(ctx(), "sess_1")
	if got.Roles[0] != "subscriber" {
		t.Errorf("Roles[0] = %q, want %q; SaveSession stored the caller's slice",
			got.Roles[0], "subscriber")
	}
}

// TestRepo_RevokeSessionStillWorks guards the operation that legitimately
// mutates stored state, so the copying change cannot silently disable it.
func TestRepo_RevokeSessionStillWorks(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	_ = r.SaveSession(ctx(), &UserSession{SessionID: "sess_1", RefreshTokenHash: rth("rt"), Roles: []string{"subscriber"}})

	if err := r.RevokeSession(ctx(), "sess_1"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	got, err := r.GetSession(ctx(), "sess_1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.IsRevoked {
		t.Error("RevokeSession did not persist; revocation is a repository " +
			"operation and must write in place")
	}
}

// TestRepo_GetGuardianDelegationReturnsACopy covers the third Get*.
//
// The delegations map has NO writer — IdentityRepository declares no
// SaveGuardianDelegation and nothing populates it — so the entry is seeded
// directly here. That gap is recorded, not fixed: adding a method would change
// the interface, which is an architectural decision this task has no authority
// to make.
func TestRepo_GetGuardianDelegationReturnsACopy(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	r.delegations["g1:s1"] = &GuardianDelegation{
		GuardianID: "g1", SubscriberID: "s1", Permissions: []string{"view"},
	}

	got, err := r.GetGuardianDelegation(ctx(), "g1", "s1")
	if err != nil {
		t.Fatalf("GetGuardianDelegation: %v", err)
	}
	got.Permissions[0] = "admin"
	got.GuardianID = "someone-else"

	again, err := r.GetGuardianDelegation(ctx(), "g1", "s1")
	if err != nil {
		t.Fatalf("GetGuardianDelegation: %v", err)
	}
	if again.Permissions[0] != "view" {
		t.Errorf("Permissions[0] = %q, want %q; a caller escalated a stored "+
			"permission through a returned pointer", again.Permissions[0], "view")
	}
	if again.GuardianID != "g1" {
		t.Errorf("GuardianID = %q, want %q", again.GuardianID, "g1")
	}
}

// TestRepo_SaveDeviceCopiesItsArgument covers the remaining Save*.
func TestRepo_SaveDeviceCopiesItsArgument(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()
	d := &DeviceAttestation{DeviceID: "dev_1", Model: "Pixel", IsPlayCertified: true}
	_ = r.SaveDevice(ctx(), d)

	d.IsPlayCertified = false
	d.Model = "tampered"

	r.mu.RLock()
	stored := r.devices["dev_1"]
	r.mu.RUnlock()

	if !stored.IsPlayCertified || stored.Model != "Pixel" {
		t.Errorf("stored device = %+v; SaveDevice kept the caller's pointer, so a "+
			"later mutation rewrote an attestation result", stored)
	}
}

// TestRepo_MissingKeysStillReportTheSameErrors pins that nothing else changed.
func TestRepo_MissingKeysStillReportTheSameErrors(t *testing.T) {
	t.Parallel()

	r := NewMemoryIdentityRepository()

	if _, err := r.GetOtp(ctx(), "nope"); !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("GetOtp missing: err = %v, want ErrInvalidOtp", err)
	}
	if _, err := r.GetSession(ctx(), "nope"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("GetSession missing: err = %v, want ErrInvalidToken", err)
	}
	if _, err := r.GetGuardianDelegation(ctx(), "nope", "nope"); !errors.Is(err, ErrGuardianForbidden) {
		t.Errorf("GetGuardianDelegation missing: err = %v, want ErrGuardianForbidden", err)
	}
	if err := r.DeleteOtp(ctx(), "nope"); err != nil {
		t.Errorf("DeleteOtp on a missing key: err = %v, want nil", err)
	}
	if err := r.RevokeSession(ctx(), "nope"); err != nil {
		t.Errorf("RevokeSession on a missing key: err = %v, want nil", err)
	}
}

// TestRepo_OtpAttemptCountingSurvivesTheChange is the end-to-end guard: the
// service must still limit attempts now that it can no longer rely on aliasing.
func TestRepo_OtpAttemptCountingSurvivesTheChange(t *testing.T) {
	t.Parallel()

	repo := NewMemoryIdentityRepository()
	svc := withFixedOtp(NewIdentityService(repo, []byte("test-secret-key")))

	reqID, err := svc.RequestOtp(ctx(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	for i := 1; i <= 2; i++ {
		if _, _, err := svc.ValidateOtp(ctx(), reqID, "000000"); err == nil {
			t.Fatalf("attempt %d: a wrong code was accepted", i)
		}
		got := storedOtp(t, repo, reqID)
		if got.Attempts != i {
			t.Fatalf("after %d wrong attempts Attempts = %d; the counter is not "+
				"being persisted through SaveOtp", i, got.Attempts)
		}
	}

	if _, _, err := svc.ValidateOtp(ctx(), reqID, "000000"); err == nil {
		t.Fatal("third wrong attempt was accepted")
	}
	if _, err := repo.GetOtp(ctx(), reqID); err == nil {
		t.Error("the challenge survived three failed attempts")
	}
}
