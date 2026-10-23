// accesstoken - EdDSA access tokens bound to a device key (ADR-0010).
//
// Shared by the identity service, which signs them, and edge-api, which
// verifies them: Go's internal-package rule kept it out of edge-api's reach
// while it lived under services/go/identity/internal. Standard library only.
module github.com/callscreen/callscreen-platform/packages/go/accesstoken

go 1.25.0
