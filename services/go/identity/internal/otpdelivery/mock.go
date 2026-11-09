package otpdelivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// # The mock provider (development only)
//
// MockProvider is a Provider that sends nothing. It hands each sign-in code to
// an in-process Outbox, where a developer or a test can read it back, so the
// whole sign-in flow can be exercised without an SMS provider.
//
// IT CHANGES NOTHING ABOUT HOW CODES ARE MADE OR CHECKED. identity still
// mints each code (crypto/rand), stores only its keyed hash, and enforces the
// expiry, the per-challenge attempt limit, single use and the per-number rate
// limits and lockout (internal/app). The mock is only where the code goes
// instead of an SMS.
//
// It is refused outside an explicitly development environment: bootstrap
// selects it only when CS_IDENTITY_ENVIRONMENT is set to "development"
// (bootstrap.ResolveOtpDelivery). Anyone who can read the outbox can sign in
// as any number, which is exactly what a development stack needs and what no
// deployed tier may have.
//
// It does not implement DeletionCodeProvider: web account-deletion codes have
// their own template, budget and decision (O13), and stay unavailable.
//
// The raw number and the code are never logged. They are kept in memory, for
// at most app.OtpLifetime, and written to the outbox file only if one is
// configured.

// MockProviderName is the mock's provider name in observations.
const MockProviderName = "mock"

// MaxOutboxEntries bounds the outbox: the least recently sent entry is evicted
// first. A development stack needs a handful.
const MaxOutboxEntries = 1024

// mockAttemptTimeout bounds each mock attempt. The mock answers at once; the
// bound exists because Sender requires one.
const mockAttemptTimeout = 2 * time.Second

// ErrOutboxWrite is a failure to append to the outbox file. It carries no
// path, number or code.
var ErrOutboxWrite = errors.New("otpdelivery: the mock outbox file could not be written")

// OutboxEntry is the most recent code sent to one number.
type OutboxEntry struct {
	Code      string
	SentAt    time.Time
	ExpiresAt time.Time
}

// Outbox holds the most recent mock code per number, in memory, and optionally
// appends each one to a file a developer can read. It is safe for concurrent
// use.
type Outbox struct {
	mu      sync.Mutex
	entries map[string]OutboxEntry
	order   []string // numbers, least recently sent first
	file    string
	now     func() time.Time
}

// NewOutbox returns an outbox. If file is not empty, each code is appended to
// that path as one line, "<expires-at RFC 3339> <number> <code>", and the file
// is created with mode 0600 if missing. An empty file keeps codes in memory
// only.
func NewOutbox(file string) *Outbox {
	return &Outbox{entries: map[string]OutboxEntry{}, file: file, now: time.Now}
}

// Latest returns the most recent unexpired code sent to msisdn.
func (o *Outbox) Latest(msisdn string) (OutboxEntry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.entries[msisdn]
	if !ok || !o.now().Before(e.ExpiresAt) {
		return OutboxEntry{}, false
	}
	return e, true
}

// Len is how many numbers the outbox holds.
func (o *Outbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.entries)
}

func (o *Outbox) put(msisdn, code string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	e := OutboxEntry{Code: code, SentAt: now, ExpiresAt: now.Add(app.OtpLifetime)}
	if o.file != "" {
		if err := appendLine(o.file, fmt.Sprintf("%s %s %s\n", e.ExpiresAt.UTC().Format(time.RFC3339), msisdn, code)); err != nil {
			return ErrOutboxWrite
		}
	}
	if _, ok := o.entries[msisdn]; ok {
		o.remove(msisdn)
	}
	o.entries[msisdn] = e
	o.order = append(o.order, msisdn)
	for len(o.order) > MaxOutboxEntries {
		delete(o.entries, o.order[0])
		o.order = o.order[1:]
	}
	return nil
}

func (o *Outbox) remove(msisdn string) {
	delete(o.entries, msisdn)
	for i, n := range o.order {
		if n == msisdn {
			o.order = append(o.order[:i], o.order[i+1:]...)
			return
		}
	}
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the path is the developer's own configuration
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close() //nolint:errcheck // the write error is the one reported
		return err
	}
	return f.Close()
}

// MockProvider is the development-only Provider: see the section above.
type MockProvider struct{ outbox *Outbox }

var _ Provider = (*MockProvider)(nil)

// NewMockProvider returns a mock provider that delivers into outbox.
func NewMockProvider(outbox *Outbox) *MockProvider { return &MockProvider{outbox: outbox} }

// Name implements Provider.
func (*MockProvider) Name() string { return MockProviderName }

// SendOtp implements Provider: it records the code in the outbox. Nothing
// leaves the process.
func (m *MockProvider) SendOtp(ctx context.Context, msisdn, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.outbox.put(msisdn, code)
}

// NewMockSender returns a Sender whose only provider is a mock delivering into
// outbox.
func NewMockSender(outbox *Outbox, observe func(provider string, outcome Outcome)) (*Sender, error) {
	return New(Config{
		Providers:      []Provider{NewMockProvider(outbox)},
		AttemptTimeout: mockAttemptTimeout,
		Observe:        observe,
	})
}
