package otpdelivery

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Twilio Programmable Messaging: a plain message whose body is the
// DLT-registered template filled with identity's code. Twilio Verify, which
// generates and checks codes itself, is NOT used: ADR-0033 Option A, and draft
// PR #271 (Verify) is not selected.
//
// The wire format follows Twilio's published Messages resource (POST
// /2010-04-01/Accounts/{AccountSid}/Messages.json, form-encoded To,
// MessagingServiceSid and Body, HTTP basic auth with an API key). Not exercised
// against Twilio: that needs an account, India DLT registration through it and
// its domestic route (R1; ADR-0033 Q9 lists the open facts).

// TwilioAPIHost is Twilio's REST API host.
const TwilioAPIHost = "api.twilio.com"

// TwilioConfig configures the Twilio adapter.
type TwilioConfig struct {
	// Name is the fixed observation label, e.g. "twilio-sms".
	Name string
	// AccountSIDFile, APIKeySIDFile and APIKeySecretFile are absolute paths of
	// mounted Secret files.
	AccountSIDFile   string
	APIKeySIDFile    string
	APIKeySecretFile string
	// MessagingServiceSID sends through a Messaging Service, which carries the
	// sender id registered for India.
	MessagingServiceSID string
	// SignInTemplate is the DLT-registered sign-in text holding {code} once.
	SignInTemplate Template
	// DeletionTemplate is the DLT-registered deletion text; empty means this
	// provider never sends deletion codes.
	DeletionTemplate Template
	// BaseURL overrides the API address; tests only (loopback over TLS).
	BaseURL string
	// HTTPClient overrides the client; tests only.
	HTTPClient *http.Client
}

var (
	twilioAccountSID = regexp.MustCompile(`^AC[0-9a-f]{32}$`)
	twilioKeySID     = regexp.MustCompile(`^SK[0-9a-f]{32}$`)
	twilioServiceSID = regexp.MustCompile(`^MG[0-9a-f]{32}$`)
)

type twilio struct {
	name, account, keySID, keySecret, service, base string
	signIn                                          Template
	client                                          *http.Client
}

type twilioWithDeletion struct {
	*twilio
	deletion Template
}

// NewTwilio builds the adapter. It reads the credential files but contacts
// nothing. It returns a DeletionCodeProvider only when DeletionTemplate is set.
func NewTwilio(cfg TwilioConfig) (Provider, error) {
	if !providerNamePattern.MatchString(cfg.Name) {
		return nil, fmt.Errorf("%w: the name must be a short lower-case label", ErrProviderConfig)
	}
	if !twilioServiceSID.MatchString(cfg.MessagingServiceSID) {
		return nil, fmt.Errorf("%w: MessagingServiceSID is not a Messaging Service SID", ErrProviderConfig)
	}
	if err := cfg.SignInTemplate.validate("sign-in"); err != nil {
		return nil, err
	}
	if cfg.DeletionTemplate != "" {
		if err := cfg.DeletionTemplate.validate("deletion"); err != nil {
			return nil, err
		}
	}
	account, err := readSecretFile("AccountSIDFile", cfg.AccountSIDFile)
	if err != nil {
		return nil, err
	}
	keySID, err := readSecretFile("APIKeySIDFile", cfg.APIKeySIDFile)
	if err != nil {
		return nil, err
	}
	secret, err := readSecretFile("APIKeySecretFile", cfg.APIKeySecretFile)
	if err != nil {
		return nil, err
	}
	if !twilioAccountSID.MatchString(account) || !twilioKeySID.MatchString(keySID) {
		return nil, fmt.Errorf("%w: the account or API key SID file holds an unexpected value", ErrProviderConfig)
	}
	base, err := endpoint(cfg.BaseURL, TwilioAPIHost)
	if err != nil {
		return nil, err
	}
	t := &twilio{
		name: cfg.Name, account: account, keySID: keySID, keySecret: secret,
		service: cfg.MessagingServiceSID, base: base, signIn: cfg.SignInTemplate,
		client: guardedClient(cfg.HTTPClient),
	}
	if cfg.DeletionTemplate != "" {
		return &twilioWithDeletion{twilio: t, deletion: cfg.DeletionTemplate}, nil
	}
	return t, nil
}

// Name returns the fixed observation label.
func (t *twilio) Name() string { return t.name }

// SendOtp delivers a sign-in code.
func (t *twilio) SendOtp(ctx context.Context, msisdn, code string) error {
	return t.send(ctx, t.signIn, msisdn, code)
}

// SendDeletionCode delivers an account-deletion code with its own template.
func (t *twilioWithDeletion) SendDeletionCode(ctx context.Context, msisdn, code string) error {
	return t.send(ctx, t.deletion, msisdn, code)
}

func (t *twilio) send(ctx context.Context, tmpl Template, msisdn, code string) error {
	national, err := domesticNumber(msisdn)
	if err != nil {
		return err
	}
	if codeErr := validCode(code); codeErr != nil {
		return codeErr
	}
	form := url.Values{
		"To":                  {"+91" + national},
		"MessagingServiceSid": {t.service},
		"Body":                {tmpl.render(code)},
	}
	u := t.base + "/2010-04-01/Accounts/" + url.PathEscape(t.account) + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrRejected
	}
	req.SetBasicAuth(t.keySID, t.keySecret)
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("accept", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProviderUnavailable
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusCreated {
		return statusError(resp.StatusCode)
	}
	return nil
}

var (
	_ Provider             = (*twilio)(nil)
	_ DeletionCodeProvider = (*twilioWithDeletion)(nil)
)
