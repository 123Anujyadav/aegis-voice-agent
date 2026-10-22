// Package app is a placeholder identity service: OTP challenges, sessions,
// device attestation and guardian delegation over an in-memory repository.
//
// It is NOT a working identity provider. RequestOtp issues the same fixed code
// every time and sends nothing, VerifyPlayIntegrity verifies nothing, and
// access tokens are unsigned hex-encoded claims. It must not guard real access.
package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/middleware"
)

// Errors returned by the identity service.
var (
	// ErrInvalidOtp reports an unknown, expired or wrong OTP code.
	ErrInvalidOtp          = errors.New("identity: invalid or expired OTP code")
	ErrInvalidToken        = errors.New("identity: invalid access or refresh token")
	ErrTokenReused         = errors.New("identity: refresh token reuse detected - revoking family session")
	ErrPlayIntegrityFailed = errors.New("identity: Google Play Integrity attestation failed")
	ErrGuardianForbidden   = errors.New("identity: guardian access denied for target subscriber")
)

// DeviceAttestation records a device's integrity attestation.
type DeviceAttestation struct {
	DeviceID        string    `json:"deviceId"`
	Model           string    `json:"model"`
	OsVersion       string    `json:"osVersion"`
	IntegrityToken  string    `json:"integrityToken"`
	AttestedAt      time.Time `json:"attestedAt"`
	IsPlayCertified bool      `json:"isPlayCertified"`
}

// UserSession is an authenticated subscriber session and its refresh token.
type UserSession struct {
	SessionID    string    `json:"sessionId"`
	SubscriberID string    `json:"subscriberId"`
	MSISDN       string    `json:"msisdn"`
	RefreshToken string    `json:"refreshToken"`
	DeviceID     string    `json:"deviceId"`
	Roles        []string  `json:"roles"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	IsRevoked    bool      `json:"isRevoked"`
}

// GuardianDelegation grants a guardian permissions over a subscriber.
type GuardianDelegation struct {
	GuardianID   string    `json:"guardianId"`
	SubscriberID string    `json:"subscriberId"`
	Permissions  []string  `json:"permissions"`
	GrantedAt    time.Time `json:"grantedAt"`
}

// OtpChallenge is a pending OTP verification, holding a keyed hash of the code.
type OtpChallenge struct {
	RequestID string    `json:"requestId"`
	MSISDN    string    `json:"msisdn"`
	CodeHash  string    `json:"codeHash"`
	Attempts  int       `json:"attempts"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// IdentityRepository stores OTP challenges, sessions, devices and delegations.
type IdentityRepository interface {
	SaveOtp(ctx context.Context, challenge *OtpChallenge) error
	GetOtp(ctx context.Context, requestID string) (*OtpChallenge, error)
	DeleteOtp(ctx context.Context, requestID string) error
	SaveSession(ctx context.Context, session *UserSession) error
	GetSession(ctx context.Context, sessionID string) (*UserSession, error)
	RevokeSession(ctx context.Context, sessionID string) error
	SaveDevice(ctx context.Context, device *DeviceAttestation) error
	GetGuardianDelegation(ctx context.Context, guardianID, subscriberID string) (*GuardianDelegation, error)
}

// MemoryIdentityRepository is an in-memory IdentityRepository, safe for
// concurrent use. Its state is lost when the process exits.
type MemoryIdentityRepository struct {
	mu          sync.RWMutex
	otps        map[string]*OtpChallenge
	sessions    map[string]*UserSession
	devices     map[string]*DeviceAttestation
	delegations map[string]*GuardianDelegation
}

// NewMemoryIdentityRepository returns an empty MemoryIdentityRepository.
func NewMemoryIdentityRepository() *MemoryIdentityRepository {
	return &MemoryIdentityRepository{
		otps:        make(map[string]*OtpChallenge),
		sessions:    make(map[string]*UserSession),
		devices:     make(map[string]*DeviceAttestation),
		delegations: make(map[string]*GuardianDelegation),
	}
}

// SaveOtp stores challenge under its request ID.
func (m *MemoryIdentityRepository) SaveOtp(ctx context.Context, challenge *OtpChallenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.otps[challenge.RequestID] = challenge
	return nil
}

// GetOtp returns the challenge for requestID, or ErrInvalidOtp.
func (m *MemoryIdentityRepository) GetOtp(ctx context.Context, requestID string) (*OtpChallenge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	otp, exists := m.otps[requestID]
	if !exists {
		return nil, ErrInvalidOtp
	}
	return otp, nil
}

// DeleteOtp removes the challenge for requestID.
func (m *MemoryIdentityRepository) DeleteOtp(ctx context.Context, requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.otps, requestID)
	return nil
}

// SaveSession stores session under its session ID.
func (m *MemoryIdentityRepository) SaveSession(ctx context.Context, session *UserSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.SessionID] = session
	return nil
}

// GetSession returns the session for sessionID, or ErrInvalidToken.
func (m *MemoryIdentityRepository) GetSession(ctx context.Context, sessionID string) (*UserSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, exists := m.sessions[sessionID]
	if !exists {
		return nil, ErrInvalidToken
	}
	return session, nil
}

// RevokeSession marks the session for sessionID revoked, if it exists.
func (m *MemoryIdentityRepository) RevokeSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, exists := m.sessions[sessionID]; exists {
		s.IsRevoked = true
	}
	return nil
}

// SaveDevice stores device under its device ID.
func (m *MemoryIdentityRepository) SaveDevice(ctx context.Context, device *DeviceAttestation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[device.DeviceID] = device
	return nil
}

// GetGuardianDelegation returns the delegation from guardianID to
// subscriberID, or ErrGuardianForbidden.
func (m *MemoryIdentityRepository) GetGuardianDelegation(ctx context.Context, guardianID, subscriberID string) (*GuardianDelegation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := guardianID + ":" + subscriberID
	del, exists := m.delegations[key]
	if !exists {
		return nil, ErrGuardianForbidden
	}
	return del, nil
}

// IdentityService implements the OTP, session and attestation flows over an
// IdentityRepository. See the package comment: it is a placeholder.
type IdentityService struct {
	repo      IdentityRepository
	secretKey []byte
}

// NewIdentityService returns an IdentityService over repo, using secretKey to
// hash OTP codes.
func NewIdentityService(repo IdentityRepository, secretKey []byte) *IdentityService {
	return &IdentityService{
		repo:      repo,
		secretKey: secretKey,
	}
}

// RequestOtp stores a new OTP challenge for msisdn and returns its request ID.
// It sends nothing, and the code is the same fixed value every time.
func (s *IdentityService) RequestOtp(ctx context.Context, msisdn string) (string, error) {
	reqID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	code := "482910"
	hash := s.hashString(code)

	challenge := &OtpChallenge{
		RequestID: reqID,
		MSISDN:    msisdn,
		CodeHash:  hash,
		Attempts:  0,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	if err := s.repo.SaveOtp(ctx, challenge); err != nil {
		return "", err
	}
	return reqID, nil
}

// ValidateOtp checks code against the challenge for requestID. On success it
// consumes the challenge and returns a new session and access token. The
// challenge is deleted once expired or after three wrong codes.
func (s *IdentityService) ValidateOtp(ctx context.Context, requestID, code string) (*UserSession, string, error) {
	challenge, err := s.repo.GetOtp(ctx, requestID)
	if err != nil {
		return nil, "", err
	}

	if time.Now().After(challenge.ExpiresAt) {
		if err := s.repo.DeleteOtp(ctx, requestID); err != nil {
			return nil, "", errors.Join(ErrInvalidOtp, err)
		}
		return nil, "", ErrInvalidOtp
	}

	if s.hashString(code) != challenge.CodeHash {
		challenge.Attempts++
		if challenge.Attempts >= 3 {
			if err := s.repo.DeleteOtp(ctx, requestID); err != nil {
				return nil, "", errors.Join(ErrInvalidOtp, err)
			}
		}
		return nil, "", ErrInvalidOtp
	}

	// A challenge that cannot be consumed could be replayed, so failing to
	// delete it fails the validation.
	if err := s.repo.DeleteOtp(ctx, requestID); err != nil {
		return nil, "", fmt.Errorf("identity: consuming OTP challenge: %w", err)
	}

	sessionID := fmt.Sprintf("sess_%d", time.Now().UnixNano())
	refreshToken := s.generateSecureToken()

	session := &UserSession{
		SessionID:    sessionID,
		SubscriberID: "usr_" + hex.EncodeToString([]byte(challenge.MSISDN))[:12],
		MSISDN:       challenge.MSISDN,
		RefreshToken: refreshToken,
		Roles:        []string{"subscriber"},
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		IsRevoked:    false,
	}

	if err := s.repo.SaveSession(ctx, session); err != nil {
		return nil, "", err
	}

	accessToken := s.generateAccessToken(session)
	return session, accessToken, nil
}

// VerifyPlayIntegrity does NOT verify the token with Google Play Integrity. It
// rejects tokens shorter than ten bytes and records a fixed attestation for
// any other token.
func (s *IdentityService) VerifyPlayIntegrity(ctx context.Context, token string) (*DeviceAttestation, error) {
	if token == "" || len(token) < 10 {
		return nil, ErrPlayIntegrityFailed
	}

	attestation := &DeviceAttestation{
		DeviceID:        "dev_" + token[:8],
		Model:           "Pixel 9 Pro",
		OsVersion:       "Android 15",
		IntegrityToken:  token,
		AttestedAt:      time.Now(),
		IsPlayCertified: true,
	}

	if err := s.repo.SaveDevice(ctx, attestation); err != nil {
		return nil, err
	}

	return attestation, nil
}

// RefreshToken rotates the session's refresh token and returns a new access
// token and refresh token. Presenting a stale refresh token revokes the
// session and returns ErrTokenReused.
func (s *IdentityService) RefreshToken(ctx context.Context, sessionID, oldRefreshToken string) (string, string, error) {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil || session.IsRevoked {
		return "", "", ErrInvalidToken
	}

	if session.RefreshToken != oldRefreshToken {
		if err := s.repo.RevokeSession(ctx, sessionID); err != nil {
			return "", "", errors.Join(ErrTokenReused, err)
		}
		return "", "", ErrTokenReused
	}

	newRefreshToken := s.generateSecureToken()
	session.RefreshToken = newRefreshToken
	// Tokens are returned only once the rotation is stored; otherwise the new
	// refresh token would not be accepted on its next use.
	if err := s.repo.SaveSession(ctx, session); err != nil {
		return "", "", fmt.Errorf("identity: storing rotated session: %w", err)
	}

	newAccessToken := s.generateAccessToken(session)
	return newAccessToken, newRefreshToken, nil
}

func (s *IdentityService) hashString(input string) string {
	h := hmac.New(sha256.New, s.secretKey)
	h.Write([]byte(input))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *IdentityService) generateSecureToken() string {
	b := make([]byte, 32)
	// A token must never be issued from an unfilled buffer.
	if _, err := rand.Read(b); err != nil {
		panic("identity: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func (s *IdentityService) generateAccessToken(session *UserSession) string {
	claims := middleware.UserClaims{
		Subject:   session.SubscriberID,
		MSISDN:    session.MSISDN,
		TenantID:  "ten_in_south",
		Roles:     session.Roles,
		ExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		// UserClaims holds only strings, a string slice and an int64.
		panic("identity: marshalling access-token claims: " + err.Error())
	}
	return "Bearer_JWT." + hex.EncodeToString(payload)
}

// IdentityHandler serves the identity HTTP endpoints.
type IdentityHandler struct {
	service *IdentityService
}

// NewIdentityHandler returns an IdentityHandler over service.
func NewIdentityHandler(service *IdentityService) *IdentityHandler {
	return &IdentityHandler{service: service}
}

// HandleRequestOtp starts an OTP challenge for the JSON body's msisdn.
func (h *IdentityHandler) HandleRequestOtp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MSISDN string `json:"msisdn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MSISDN == "" {
		middleware.WriteProblemDetails(w, http.StatusBadRequest, "Bad Request", "Invalid MSISDN input", "ERR_INVALID_INPUT", r.URL.Path, "")
		return
	}

	reqID, err := h.service.RequestOtp(r.Context(), req.MSISDN)
	if err != nil {
		middleware.WriteProblemDetails(w, http.StatusInternalServerError, "Internal Error", err.Error(), "ERR_INTERNAL", r.URL.Path, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck // the status line is already written, so an encoding error cannot reach the client
		"requestId": reqID,
		"expiresIn": 300,
		"status":    "SENT",
	})
}

// HandleValidateOtp validates the JSON body's OTP code and returns the session
// tokens.
func (h *IdentityHandler) HandleValidateOtp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string `json:"requestId"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == "" || req.Code == "" {
		middleware.WriteProblemDetails(w, http.StatusBadRequest, "Bad Request", "Missing request ID or OTP code", "ERR_INVALID_INPUT", r.URL.Path, "")
		return
	}

	session, token, err := h.service.ValidateOtp(r.Context(), req.RequestID, req.Code)
	if err != nil {
		middleware.WriteProblemDetails(w, http.StatusUnauthorized, "Unauthorized", err.Error(), "ERR_UNAUTHORIZED", r.URL.Path, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck // the status line is already written, so an encoding error cannot reach the client
		"accessToken":  token,
		"refreshToken": session.RefreshToken,
		"sessionId":    session.SessionID,
		"subscriberId": session.SubscriberID,
	})
}
