package app

import (
	"context"
	"crypto/hmac"
	"errors"
	"time"
)

// Web account deletion (docs/phase19/ACCOUNT-DELETION.md §3): a person proves
// control of the phone number with a one-time code and the account is
// erased, without the app. Two steps:
//
//  1. RequestDeletionCode(msisdn) answers the SAME for every valid number:
//     a request id, whether or not the number has an account. A code is SENT
//     only when it has one (an account with nothing stored has nothing to
//     delete), which spends nothing on numbers without an account and makes
//     the endpoint worthless for SMS pumping to arbitrary numbers. For a
//     number with no account the challenge still exists, with the hash of a
//     code nobody receives, so every later step behaves the same. The send
//     is dispatched after the answer is decided, so its latency does not
//     reveal the difference.
//  2. ConfirmDeletion(requestID, code) checks the code exactly as sign-in
//     does (checkCode: attempts, expiry, the number's shared lockout,
//     constant-time comparison), and only an account-deletion code is
//     accepted: a sign-in code cannot delete, and a deletion code cannot sign
//     in. The challenge is consumed INSIDE the erasure transaction, so of
//     concurrent confirms one erases and the others are answered from its
//     receipt. A retry after a lost answer is answered from the same receipt.
//
// LIMITS: the number's existing OTP request limit and failed-verification
// lockout apply, SHARED with sign-in (the same pseudonym key), and a global
// hourly budget caps deletion-code requests across all numbers (counted
// whether or not a code is sent, so it reveals nothing about a number).
//
// DELIVERY: only a DeletionCodeSender delivers deletion codes. No provider
// implements one (production configures no OTP sender at all), so without
// one, and without a budget, every request is ErrDeletionCodesUnavailable,
// for every number alike. The end-to-end harness's stdout sender is
// test-only.

// ErrDeletionCodesUnavailable means deletion codes cannot be issued now: no
// deletion-code sender or no budget is configured, or this hour's budget is
// spent. It never depends on the number.
var ErrDeletionCodesUnavailable = errors.New("account-deletion codes are unavailable")

// DeletionCodeSender delivers an account-deletion code. It is separate from
// OtpSender because India DLT requires each message template to be registered:
// a deletion message is not a sign-in message.
type DeletionCodeSender interface {
	SendDeletionCode(ctx context.Context, msisdn, code string) error
}

// WebErasureRepository is the storage web deletion needs beyond
// ErasureRepository. The pgstore and memory repositories implement it.
type WebErasureRepository interface {
	ErasureRepository
	// HasAccount reports whether identity holds any session of subscriberID.
	HasAccount(ctx context.Context, subscriberID string) (bool, error)
	// ReserveDeletionCodeRequest counts one request in the hour starting at
	// window and reports whether the hour's count is within limit.
	ReserveDeletionCodeRequest(ctx context.Context, window time.Time, limit int) (bool, error)
	// EraseSubscriberByDeletionCode consumes the account-deletion challenge
	// requestID of e.SubscriberID and erases the subscriber, in one
	// transaction, writing a receipt (requestID, codeHash) until
	// e.ReceiptExpiresAt. If the challenge is already gone (a concurrent
	// confirm consumed it) it changes nothing and returns ErrSessionConflict.
	EraseSubscriberByDeletionCode(ctx context.Context, e *SubscriberErasure, requestID, codeHash string) error
	// DeletionCodeReceipt returns the code hash of a completed deletion's
	// receipt, and false if there is none (never completed, or expired).
	DeletionCodeReceipt(ctx context.Context, requestID string) (codeHash string, found bool, err error)
}

// SetDeletionCodeSender configures delivery of account-deletion codes.
// Construction-time configuration, like SetOtpSender.
func (s *IdentityService) SetDeletionCodeSender(d DeletionCodeSender) { s.deletionCodes = d }

// SetDeletionCodeBudget sets the global hourly cap on deletion-code requests.
// Zero (the default) leaves deletion codes unavailable: the cap is an
// operational decision, not one this package makes.
func (s *IdentityService) SetDeletionCodeBudget(perHour int) { s.deletionBudget = perHour }

// dispatchDeletionCode runs a deletion-code send after the answer is decided.
// Tests replace it to run synchronously.
func (s *IdentityService) dispatchDeletionCode(send func()) {
	if s.deletionDispatch != nil {
		s.deletionDispatch(send)
		return
	}
	go send()
}

func (s *IdentityService) webErasure() (WebErasureRepository, bool) {
	repo, ok := s.repo.(WebErasureRepository)
	return repo, ok && s.deletionCodes != nil && s.deletionBudget > 0
}

// RequestDeletionCode starts a web deletion for msisdn: see the package
// documentation above. The answer reveals nothing about whether the number
// has an account.
func (s *IdentityService) RequestDeletionCode(ctx context.Context, msisdn string) (string, error) {
	repo, ok := s.webErasure()
	if !ok {
		return "", ErrDeletionCodesUnavailable
	}
	if !isCanonicalMSISDN(msisdn) {
		return "", ErrInvalidMSISDN
	}
	pseudonym, err := s.msisdnPseudonym(msisdn)
	if err != nil {
		return "", err
	}
	// The number's own request limit, shared with sign-in.
	now := s.now()
	if rerr := s.repo.ReserveOtpRequest(ctx, pseudonym, now, s.ratePolicy); rerr != nil {
		return "", rerr
	}
	// The global hourly budget, counted for every request alike.
	within, err := repo.ReserveDeletionCodeRequest(ctx, now.UTC().Truncate(time.Hour), s.deletionBudget)
	if err != nil {
		return "", err
	}
	if !within {
		return "", ErrDeletionCodesUnavailable
	}

	subscriberID := s.subscriberID(msisdn)
	hasAccount, err := repo.HasAccount(ctx, subscriberID)
	if err != nil {
		return "", err
	}
	reqID := "req_" + s.generateSecureToken()
	// The challenge exists either way; for a number with no account its code
	// is simply never sent.
	code := s.newCode()
	challenge := &OtpChallenge{
		RequestID:       reqID,
		Purpose:         OtpPurposeAccountDeletion,
		MSISDNPseudonym: pseudonym,
		SubscriberID:    subscriberID,
		CodeHash:        s.hashString(code),
		ExpiresAt:       time.Now().Add(OtpLifetime),
	}
	if err := s.repo.SaveOtp(ctx, challenge); err != nil {
		return "", err
	}
	if hasAccount {
		sender := s.deletionCodes
		s.dispatchDeletionCode(func() {
			// Detached from the request, which has already been answered,
			// and bounded. A code that could not be handed on is withdrawn;
			// the sender's error (provider text) goes nowhere.
			sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := sender.SendDeletionCode(sendCtx, msisdn, code); err != nil {
				_ = s.repo.DeleteOtp(sendCtx, reqID) //nolint:errcheck // best effort: an undelivered code expires anyway
			}
		})
	}
	return reqID, nil
}

// ConfirmDeletion erases the account whose deletion code this is. Success is
// nil; a wrong, expired, unknown or other-purpose code is refused exactly as
// at sign-in, and counts toward the number's lockout where sign-in's would.
func (s *IdentityService) ConfirmDeletion(ctx context.Context, requestID, code string) error {
	repo, ok := s.webErasure()
	if !ok {
		return ErrDeletionCodesUnavailable
	}
	challenge, err := s.repo.GetOtp(ctx, requestID)
	if errors.Is(err, ErrInvalidOtp) {
		// Gone: never existed, expired and swept, or already used to delete.
		return deletionReceiptAnswer(ctx, repo, requestID, s.hashString(code))
	}
	if err != nil {
		return err
	}
	if challenge.Purpose != OtpPurposeAccountDeletion {
		return ErrInvalidOtp
	}
	reservationID, err := s.checkCode(ctx, challenge, requestID, code)
	if err != nil {
		return err
	}

	now := time.Now()
	err = repo.EraseSubscriberByDeletionCode(ctx, &SubscriberErasure{
		SubscriberID:     challenge.SubscriberID,
		RequestedAt:      now,
		ReceiptExpiresAt: challenge.ExpiresAt,
		Exceptions:       ErasureExceptions,
	}, requestID, challenge.CodeHash)
	if err != nil {
		// Nothing was consumed or erased: the reservation is withdrawn.
		if rerr := s.repo.ReleaseOtpVerification(ctx, challenge.MSISDNPseudonym, reservationID); rerr != nil {
			return errors.Join(err, rerr)
		}
		if errors.Is(err, ErrSessionConflict) {
			// A concurrent confirm consumed it first: answered as that one was.
			return deletionReceiptAnswer(ctx, repo, requestID, challenge.CodeHash)
		}
		return err
	}
	// The erasure is committed; a failure to reset the number's failure count
	// cannot undo it, and only leaves the count to expire.
	_ = s.repo.ResetOtpFailures(ctx, challenge.MSISDNPseudonym) //nolint:errcheck // the erasure stands either way
	// Audited as an in-app deletion is, with the request's random id.
	s.audit(SecurityEvent{Type: EventAccountDeleted, At: now, RequestID: requestID})
	return nil
}

// deletionReceiptAnswer: a success only for a completed deletion's request id
// with its code (a retry after a lost answer); anything else is ErrInvalidOtp,
// the same as a code that never existed.
func deletionReceiptAnswer(ctx context.Context, repo WebErasureRepository, requestID, codeHash string) error {
	stored, found, err := repo.DeletionCodeReceipt(ctx, requestID)
	if err != nil {
		return err
	}
	if !found || !hmac.Equal([]byte(stored), []byte(codeHash)) {
		return ErrInvalidOtp
	}
	return nil
}
