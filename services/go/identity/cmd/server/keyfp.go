package main

import (
	"fmt"
	"io"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/bootstrap"
)

// writeSigningKeyFingerprint prints the signing key's kid and the fingerprint
// of its public half (`service signing-key-fingerprint`), reading only
// CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE and CS_IDENTITY_ACCESS_TOKEN_KID.
// The output is public information: compare it with edge-api's
// `service trusted-keys` before each rotation step.
func writeSigningKeyFingerprint(out io.Writer) error {
	kid, fp, err := bootstrap.SigningKeyFingerprint()
	if err != nil {
		return fmt.Errorf("signing-key-fingerprint: %w", err)
	}
	if _, err := fmt.Fprintf(out, "signing kid=%s sha256=%s\n", kid, fp); err != nil {
		return fmt.Errorf("signing-key-fingerprint: writing the result: %w", err)
	}
	return nil
}
