# Ollama endpoint policy: loopback only (2026-11-10)

- **Status:** **Proposed: decided under owner delegation** (`ARCHITECTURE_FREEZE.md` §6.1 rule 7). It stays Proposed until the owner ratifies it, and the owner may reclassify it (rule 6). Implemented in `packages/go/voice/providers/ollama/endpoint.go`.
- **Classification: routine, not high-risk,** because:
  - it narrows a development-only adapter to fail closed;
  - no service uses the adapter (`ollama` is explicitly not a production tier, and no service builds a `runtime.Kernel`);
  - it changes no frozen invariant, ADR, authentication model or personal-data flow of any running system, and is reversible.
  - **Making it permissive again would not be routine** (§4).
- **Why:** a prompt carries a caller's transcript, which is personal data (ADR-0006 security; ADR-0012). Before this policy, any `http` or `https` host was accepted, so a configuration mistake could send transcripts to another machine. That is a residency and DPDP question (ADR-0012, E9, L1), not a configuration detail.

## 1. The rule

The adapter talks only to a daemon on the same machine. Every layer fails closed.

| Layer | What is enforced | Closes |
|---|---|---|
| **Configuration** (`New`) | The `BaseURL` host must be exactly `localhost`, `127.0.0.1` or `::1`. The URL carries no credentials. The scheme is `http` or `https`; the path may carry a prefix | A remote host, a private name, another loopback spelling (`127.0.0.2`, `::ffff:127.0.0.1`), a public name that resolves to loopback (`127.0.0.1.nip.io`), `0.0.0.0` |
| **Dialling** (the adapter's own client) | The connection is refused unless the **resolved** address is a loopback IP (`net.Dialer.Control`, which runs after name resolution) | A hosts file or resolver that maps `localhost` elsewhere |
| **Proxy** (the adapter's own client) | None: `Transport.Proxy` is nil, so `HTTP_PROXY` and `HTTPS_PROXY` are ignored | A proxy carrying the prompt off the machine |
| **Redirects** (every client) | Never followed. A supplied client is copied with redirects refused, never mutated | A 307 or 308 re-sending the prompt to the location it names |

Refusals are `ErrNonLoopbackEndpoint` (dialling, redirects) or a configuration error naming `BaseURL` (construction). An error never echoes credentials from the URL.

## 2. What it does not cover

- **A caller-supplied `HTTPClient` keeps its own transport.** Its URL is still validated and it never follows a redirect, but its dialling and proxy settings are the caller's. Today only tests supply one.
- **What listens on the loopback port** is not authenticated. Any local process can answer on port 11434. That is inherent to a local daemon, and the reason this adapter is not a production tier.

## 3. Tests

In `providers/ollama/endpoint_test.go`. Each test fails if its layer is removed (mutation-checked on 2026-11-10):
- loopback hosts accepted;
- eleven other hosts refused at construction, without echoing credentials;
- the dialler refuses resolved non-loopback IPv4 and IPv6 addresses and accepts a loopback listener;
- no proxy on the adapter's client;
- 302, 307 and 308 redirects are never followed, by the adapter's client or a supplied one, and the redirect target receives nothing;
- a supplied client is not mutated.

## 4. Changing it

A remote endpoint (a hosted model, a GPU server, another developer's machine) is **not** a flag on this adapter. It needs its own approved policy covering:
- residency (ADR-0012: India unless the four cross-border conditions hold);
- a DPA with no retention or training;
- consent;
- transport security and authentication to that endpoint;
- the E9 and L1 review.

That decision changes where personal data goes, so it is **high-risk** under `docs/adr/README.md`. It needs the owner's decision **and** an independent human review (OD-12, rule 8) before any code accepts a non-loopback host.

**No activation:** this policy starts no daemon, pulls no model and contacts nothing.
