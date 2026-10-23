package main

import (
	"fmt"
	"io"

	"github.com/callscreen/callscreen-platform/services/go/edge-api/internal/auth"
)

// writeTrustedKeys prints one line per trusted key (`service trusted-keys`):
// its role, kid and public fingerprint. The output is public information:
// compare it with identity's `service signing-key-fingerprint` before each
// rotation step.
func writeTrustedKeys(out io.Writer) error {
	keys, err := auth.TrustedKeyFingerprints()
	if err != nil {
		return fmt.Errorf("trusted-keys: %w", err)
	}
	for _, k := range keys {
		if _, err := fmt.Fprintf(out, "%s kid=%s sha256=%s\n", k.Role, k.KeyID, k.Fingerprint); err != nil {
			return fmt.Errorf("trusted-keys: writing the result: %w", err)
		}
	}
	return nil
}
