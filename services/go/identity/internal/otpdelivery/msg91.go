package otpdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// MSG91: delivery through a registered "flow" (a DLT template with the code
// as its variable). MSG91's own OTP API, which generates and verifies codes, is
// deliberately NOT used: under ADR-0033 the provider only delivers.
//
// The wire format follows MSG91's published "Send SMS" API, re-verified
// against https://docs.msg91.com/sms/send-sms on 2026-11-13 (record:
// docs/phase19/MSG91-INTEGRATION.md §3): POST
// https://control.msg91.com/api/v5/flow with the "authkey", "content-type"
// and "accept" headers; a JSON body of template_id, short_url,
// realTimeResponse and recipients, each recipient being "mobiles" in
// international format without "+" plus the template's variables, whose names
// are case-sensitive; and the answer {"type":"success","message":"<request
// id>"}. The request id is what MSG91's SMS Logs API and delivery reports are
// keyed by.
//
// It has NOT been exercised against MSG91: that needs an account, a
// DLT-registered template and the owner's approval of one test transaction
// (R1). A "success" answer means MSG91 accepted the request, not that the
// handset received the message; delivery is read from MSG91's logs by the
// request id.
//
// One request per call, never retried here (Provider's contract): a request
// that timed out may still have been accepted, and repeating it could deliver
// the code twice.

// MSG91APIHost is MSG91's API host.
const MSG91APIHost = "control.msg91.com"

// MSG91Config configures the MSG91 adapter.
type MSG91Config struct {
	// Name is the fixed observation label, e.g. "msg91".
	Name string
	// AuthKeyFile is the absolute path of the mounted file holding the auth key.
	AuthKeyFile string
	// SignInFlowID is the flow (template) id for the sign-in message.
	SignInFlowID string
	// DeletionFlowID is the flow id for the account-deletion message; empty
	// means this provider never sends deletion codes.
	DeletionFlowID string
	// CodeVariable is the template variable that receives the code (e.g.
	// "otp"). The flow's text is registered with MSG91 and DLT, so the adapter
	// sends only the code, never text.
	CodeVariable string
	// Accepted, if set, receives MSG91's request id for each accepted message,
	// so the message can be traced in MSG91's logs. The id carries neither the
	// number nor the code; an answer without a well-formed id is still
	// accepted, but reports nothing. It must be safe for concurrent use and
	// must not block.
	Accepted func(requestID string)
	// BaseURL overrides the API address; tests only (loopback over TLS).
	BaseURL string
	// HTTPClient overrides the client; tests only.
	HTTPClient *http.Client
}

var msg91IDPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

type msg91 struct {
	name, key, base, signInFlow, variable string
	client                                *http.Client
	accepted                              func(requestID string)
}

type msg91WithDeletion struct {
	*msg91
	deletionFlow string
}

// NewMSG91 builds the adapter. It reads the key file but contacts nothing. It
// returns a DeletionCodeProvider only when DeletionFlowID is set.
func NewMSG91(cfg MSG91Config) (Provider, error) {
	if !providerNamePattern.MatchString(cfg.Name) {
		return nil, fmt.Errorf("%w: the name must be a short lower-case label", ErrProviderConfig)
	}
	if !msg91IDPattern.MatchString(cfg.SignInFlowID) ||
		(cfg.DeletionFlowID != "" && !msg91IDPattern.MatchString(cfg.DeletionFlowID)) {
		return nil, fmt.Errorf("%w: flow ids must be 1-64 letters or digits", ErrProviderConfig)
	}
	if !msg91IDPattern.MatchString(cfg.CodeVariable) {
		return nil, fmt.Errorf("%w: CodeVariable must be 1-64 letters or digits", ErrProviderConfig)
	}
	key, err := readSecretFile("AuthKeyFile", cfg.AuthKeyFile)
	if err != nil {
		return nil, err
	}
	base, err := endpoint(cfg.BaseURL, MSG91APIHost)
	if err != nil {
		return nil, err
	}
	m := &msg91{
		name: cfg.Name, key: key, base: base, signInFlow: cfg.SignInFlowID,
		variable: cfg.CodeVariable, client: guardedClient(cfg.HTTPClient), accepted: cfg.Accepted,
	}
	if cfg.DeletionFlowID != "" {
		return &msg91WithDeletion{msg91: m, deletionFlow: cfg.DeletionFlowID}, nil
	}
	return m, nil
}

// Name returns the fixed observation label.
func (m *msg91) Name() string { return m.name }

// SendOtp delivers a sign-in code through the sign-in flow.
func (m *msg91) SendOtp(ctx context.Context, msisdn, code string) error {
	return m.send(ctx, m.signInFlow, msisdn, code)
}

// SendDeletionCode delivers an account-deletion code through its own flow.
func (m *msg91WithDeletion) SendDeletionCode(ctx context.Context, msisdn, code string) error {
	return m.send(ctx, m.deletionFlow, msisdn, code)
}

func (m *msg91) send(ctx context.Context, flow, msisdn, code string) error {
	national, err := domesticNumber(msisdn)
	if err != nil {
		return err
	}
	if codeErr := validCode(code); codeErr != nil {
		return codeErr
	}
	body, err := json.Marshal(map[string]any{
		"template_id": flow,
		"short_url":   "0",
		// MSG91: "provides a real-time response ... addressing most API error
		// codes", so a refusal is answered now rather than only by email.
		"realTimeResponse": "1",
		"recipients":       []map[string]string{{"mobiles": "91" + national, m.variable: code}},
	})
	if err != nil {
		return ErrRejected
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/api/v5/flow", bytes.NewReader(body))
	if err != nil {
		return ErrRejected
	}
	req.Header.Set("authkey", m.key)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProviderUnavailable
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	// MSG91 can answer 200 with {"type":"error"}: only "success" is delivery.
	var answer struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&answer); err != nil ||
		answer.Type != "success" {
		return ErrRejected
	}
	// On success "message" is the request id. It is passed on only when it
	// has the shape of one: anything else might be provider text.
	if m.accepted != nil && msg91IDPattern.MatchString(answer.Message) {
		m.accepted(answer.Message)
	}
	return nil
}

var (
	_ Provider             = (*msg91)(nil)
	_ DeletionCodeProvider = (*msg91WithDeletion)(nil)
)
