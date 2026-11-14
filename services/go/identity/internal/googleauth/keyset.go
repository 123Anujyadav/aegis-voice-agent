package googleauth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// keySet is Google's signing keys, fetched and cached with bounded network
// behaviour (owner decisions of 2026-11-13 and 2026-11-14):
//
//   - The cache lasts exactly as long as Google's Cache-Control max-age says,
//     never longer than MaxKeyCacheTTL, and is never stretched: a short or
//     missing max-age (or no-store, no-cache) caches for that long or not at
//     all.
//   - Expired keys are not used. A verification that finds them expired
//     fetches again; concurrent verifications share that one fetch.
//   - A key id the fresh cache does not hold triggers a refetch at most once
//     per UnknownKeyRefetchInterval; inside that interval it is refused.
//   - Each fetch: TLS, no redirect, FetchTimeout, a body of at most
//     MaxKeySetBytes, application/json, RSA signing keys of at least 2048
//     bits only.
//   - No usable key (the fetch failed and nothing fresh is cached) is
//     errUnavailable: fail closed.
type keySet struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	lastFetch time.Time
}

// Bounds of the key cache and its fetches.
const (
	MaxKeyCacheTTL            = 24 * time.Hour
	UnknownKeyRefetchInterval = time.Minute
	FetchTimeout              = 5 * time.Second
	MaxKeySetBytes            = 64 << 10
	minRSABits                = 2048
)

var (
	// No usable key: the verifier answers unavailable.
	errUnavailable = errors.New("googleauth: Google's signing keys are unavailable")
	// A key id that no fresh key set holds: a refusal.
	errUnknownKey = errors.New("googleauth: unknown signing key")
)

// lookup returns the key for kid, fetching as the rules above allow.
func (k *keySet) lookup(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	fresh := now.Before(k.expires)
	if fresh {
		if key, ok := k.keys[kid]; ok {
			return key, nil
		}
		if now.Sub(k.lastFetch) < UnknownKeyRefetchInterval {
			return nil, errUnknownKey
		}
	}
	// Fetch (the lock makes concurrent callers wait for this one fetch). A
	// fetch that fails leaves fresh keys in place for other key ids.
	keys, ttl, err := k.fetch(ctx)
	k.lastFetch = now
	if err != nil {
		return nil, errUnavailable
	}
	k.keys, k.expires = keys, now.Add(ttl)
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, errUnknownKey
}

// fetch downloads and parses the key set, and returns it with its cache TTL.
func (k *keySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("googleauth: key set status %d", resp.StatusCode)
	}
	if mt, _, perr := mime.ParseMediaType(resp.Header.Get("Content-Type")); perr != nil || mt != "application/json" {
		return nil, 0, errors.New("googleauth: key set is not JSON")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxKeySetBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > MaxKeySetBytes {
		return nil, 0, errors.New("googleauth: key set too large")
	}
	keys, err := parseKeySet(body)
	if err != nil {
		return nil, 0, err
	}
	return keys, cacheTTL(resp.Header.Values("Cache-Control")), nil
}

// parseKeySet keeps the RSA signing keys of at least 2048 bits, by key id.
func parseKeySet(body []byte) (map[string]*rsa.PublicKey, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, errors.New("googleauth: key set does not parse")
	}
	keys := map[string]*rsa.PublicKey{}
	for i := range set.Keys {
		jwk := &set.Keys[i]
		pub, ok := jwk.Key.(*rsa.PublicKey)
		if !ok || jwk.KeyID == "" || pub.N == nil || pub.N.BitLen() < minRSABits ||
			(jwk.Use != "" && jwk.Use != "sig") || (jwk.Algorithm != "" && jwk.Algorithm != string(jose.RS256)) {
			continue
		}
		if _, dup := keys[jwk.KeyID]; dup {
			return nil, errors.New("googleauth: key set repeats a key id")
		}
		keys[jwk.KeyID] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("googleauth: key set has no usable key")
	}
	return keys, nil
}

// cacheTTL is the response's max-age, bounded by MaxKeyCacheTTL; zero when
// absent, malformed, or when no-store or no-cache is present. It is never
// raised above what Google said.
func cacheTTL(values []string) time.Duration {
	var maxAge time.Duration
	found := false
	for _, v := range values {
		for _, directive := range strings.Split(v, ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "no-store", "no-cache":
				return 0
			case "max-age":
				secs, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(value), `"`), 10, 64)
				if err != nil || secs < 0 {
					return 0
				}
				if secs > int64(MaxKeyCacheTTL/time.Second) {
					secs = int64(MaxKeyCacheTTL / time.Second)
				}
				maxAge, found = time.Duration(secs)*time.Second, true
			}
		}
	}
	if !found {
		return 0
	}
	return maxAge
}
