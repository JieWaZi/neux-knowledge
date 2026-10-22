package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/JieWaZi/neux-knowledge/core"
	"net"
	"net/netip"
	"strings"
	"sync"
)

// LibraryGrant authorizes one explicit knowledge library; the zero value denies access.
type LibraryGrant struct {
	Operations    []string // Explicitly permitted read, write, organize, maintain, and purge operations; permanent cleanup requires separate authorization.
	Scope         []string // Actual project scope verified by the server.
	PolicyVersion int64    // Must match the library's current authorization generation.
}

func credentialHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// RestoreGuard restores a publication guard from a trusted host's persistent identity and rejects publication if the original credential was revoked or its scope changed.
func (s *PolicyStore) RestoreGuard(e *core.Engine, id RecoveryIdentity) func(context.Context) (func(), error) {
	id.Permission.Scope = append([]string(nil), id.Permission.Scope...)
	id.Permission.Operations = append([]string(nil), id.Permission.Operations...)
	return func(ctx context.Context) (func(), error) {
		if err := ctx.Err(); err != nil {
			return nil, &APIError{Code: "cancelled", Status: 499}
		}
		if s == nil || e == nil || id.Subject == "" || id.LibraryID == "" || len(id.CredentialSHA256) != 64 || (id.Operation != "organize" && id.Operation != "maintain") {
			return nil, &APIError{Code: "forbidden", Status: 403}
		}
		s.mu.RLock()
		fail := func() (func(), error) { s.mu.RUnlock(); return nil, &APIError{Code: "policy_changed", Status: 403} }
		var principal Principal
		found := false
		for token, p := range s.config.Tokens {
			if credentialHash(token) == id.CredentialSHA256 && p.Subject == id.Subject {
				principal = p
				found = true
				break
			}
		}
		if !found {
			return fail()
		}
		g := principal.Libraries[id.LibraryID]
		if !contains(g.Operations, id.Operation) || g.PolicyVersion != id.Permission.PolicyVersion || !sameStrings(g.Scope, id.Permission.Scope) || !sameStrings(g.Operations, id.Permission.Operations) {
			return fail()
		}
		if s.config.WriteIPPolicy.Enabled {
			ip, ok := clientIP(id.RemoteAddr, id.ForwardedFor, s.config.WriteIPPolicy)
			if !ok || !inPrefixes(ip, s.config.WriteIPPolicy.Allowed) {
				return fail()
			}
		}
		l, err := e.GetLibrary(ctx, id.LibraryID)
		if err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		if l.PolicyVersion != g.PolicyVersion {
			return fail()
		}
		return s.mu.RUnlock, nil
	}
}

// Principal is the server-configured principal and library scope, never read from the HTTP body.
type Principal struct {
	// ActivityActor can explicitly identify an automatic trigger on the server; when omitted, the authenticated Subject is used.
	ActivityActor core.ActivityActor
	Subject       string                  // Stable authenticated principal.
	Libraries     map[string]LibraryGrant // Per-library grants without implicit wildcards.
	CreateLibrary bool                    // Whether creating libraries is separately allowed; creation does not automatically expand existing grants.
}

// IPPolicy constrains all remote write entry points and in-flight generated publications.
type IPPolicy struct {
	Enabled        bool           // When enabled with an empty Allowed list, all writes are rejected.
	Allowed        []netip.Prefix // Allowlist of actual client origin addresses.
	TrustedProxies []netip.Prefix // Explicitly trusted proxies; only trusted TCP peers may supply a forwarding chain.
}

// SecurityConfig must be atomically replaced through Replace; callers may persist it in a controlled configuration service.
type SecurityConfig struct {
	Tokens        map[string]Principal // Independent remote credentials without reusing host administration tokens.
	WriteIPPolicy IPPolicy             // Current write-origin policy.
}

// PolicyStore serializes dynamic revocation and actual publication commits using the same read-write lock.
type PolicyStore struct {
	mu     sync.RWMutex   // Publication holds a read lock; policy changes hold a write lock.
	config SecurityConfig // Private deep copy prevents external mutable references from bypassing the lock.
}

func NewPolicyStore(c SecurityConfig) (*PolicyStore, error) {
	s := &PolicyStore{}
	if err := s.Replace(c); err != nil {
		return nil, err
	}
	return s, nil
}

// After Replace returns, all subsequently acquired guards use the new policy; publications with earlier guards have completed.
func (s *PolicyStore) Replace(c SecurityConfig) error {
	clone := SecurityConfig{Tokens: map[string]Principal{}, WriteIPPolicy: c.WriteIPPolicy}
	clone.WriteIPPolicy.Allowed = append([]netip.Prefix(nil), c.WriteIPPolicy.Allowed...)
	clone.WriteIPPolicy.TrustedProxies = append([]netip.Prefix(nil), c.WriteIPPolicy.TrustedProxies...)
	for _, p := range append(append([]netip.Prefix(nil), clone.WriteIPPolicy.Allowed...), clone.WriteIPPolicy.TrustedProxies...) {
		if !p.IsValid() {
			return errors.New("invalid IP prefix")
		}
	}
	for token, p := range c.Tokens {
		if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \r\n\t") || strings.TrimSpace(p.Subject) == "" {
			return errors.New("invalid credential")
		}
		q := Principal{ActivityActor: p.ActivityActor, Subject: p.Subject, CreateLibrary: p.CreateLibrary, Libraries: map[string]LibraryGrant{}}
		for id, g := range p.Libraries {
			if id == "" || g.PolicyVersion < 1 {
				return errors.New("invalid library grant")
			}
			g.Operations = append([]string(nil), g.Operations...)
			g.Scope = append([]string(nil), g.Scope...)
			for _, op := range g.Operations {
				if op != "read" && op != "write" && op != "organize" && op != "maintain" && op != "purge" {
					return errors.New("invalid operation")
				}
			}
			q.Libraries[id] = g
		}
		clone.Tokens[token] = q
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = clone
	return nil
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
func clientIP(remote, forwarded string, p IPPolicy) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return netip.Addr{}, false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	peer = peer.Unmap()
	if !inPrefixes(peer, p.TrustedProxies) {
		return peer, true
	}
	if forwarded == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 32 {
		return netip.Addr{}, false
	}
	ips := make([]netip.Addr, len(parts))
	for i, x := range parts {
		a, e := netip.ParseAddr(strings.TrimSpace(x))
		if e != nil {
			return netip.Addr{}, false
		}
		ips[i] = a.Unmap()
	}
	for i := len(ips) - 1; i >= 0; i-- {
		if !inPrefixes(ips[i], p.TrustedProxies) {
			return ips[i], true
		}
	}
	return netip.Addr{}, false
}
func (s *PolicyStore) acquire(ctx context.Context, token, library, op, remote, forwarded string) (Principal, LibraryGrant, func(), error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, LibraryGrant{}, nil, &APIError{Code: "cancelled", Status: 499}
	}
	if s == nil {
		return Principal{}, LibraryGrant{}, nil, &APIError{Code: "unauthenticated", Status: 401}
	}
	s.mu.RLock()
	fail := func(code string, status int) (Principal, LibraryGrant, func(), error) {
		s.mu.RUnlock()
		return Principal{}, LibraryGrant{}, nil, &APIError{Code: code, Status: status}
	}
	p, ok := s.config.Tokens[token]
	if !ok {
		return fail("unauthenticated", 401)
	}
	g := p.Libraries[library]
	if op == "create" {
		if !p.CreateLibrary {
			return fail("forbidden", 403)
		}
	} else if op != "list" && !contains(g.Operations, op) {
		return fail("forbidden", 403)
	}
	if op != "read" && op != "list" && s.config.WriteIPPolicy.Enabled {
		ip, ok := clientIP(remote, forwarded, s.config.WriteIPPolicy)
		if !ok || !inPrefixes(ip, s.config.WriteIPPolicy.Allowed) {
			return fail("write_ip_forbidden", 403)
		}
	}
	return p, g, s.mu.RUnlock, nil
}
