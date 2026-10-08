// Package permissions provides domain-local role/permission helpers.
//
// Every user-domain service defines its own roles and permissions; nothing
// here assigns global meaning to a role name. A token from one domain never
// carries authority in another domain.
package permissions

import (
	"sort"
	"strings"
)

// Permission is a domain-local permission name, e.g. "listing:publish".
type Permission string

// Role is a domain-local role name.
type Role string

// Set is an immutable-by-convention set of permissions.
type Set map[Permission]struct{}

// NewSet builds a set.
func NewSet(ps ...Permission) Set {
	s := make(Set, len(ps))
	for _, p := range ps {
		s[p] = struct{}{}
	}
	return s
}

// Has reports whether p is granted.
func (s Set) Has(p Permission) bool { _, ok := s[p]; return ok }

// HasAll reports whether every permission is granted.
func (s Set) HasAll(ps ...Permission) bool {
	for _, p := range ps {
		if !s.Has(p) {
			return false
		}
	}
	return true
}

// HasAny reports whether at least one permission is granted.
func (s Set) HasAny(ps ...Permission) bool {
	for _, p := range ps {
		if s.Has(p) {
			return true
		}
	}
	return false
}

// List returns the sorted permissions.
func (s Set) List() []Permission {
	out := make([]Permission, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RoleMap maps a domain's roles to permissions.
type RoleMap map[Role][]Permission

// Resolve returns the union of permissions for roles. Unknown roles grant
// nothing (fail closed).
func (m RoleMap) Resolve(roles ...Role) Set {
	s := Set{}
	for _, r := range roles {
		for _, p := range m[r] {
			s[p] = struct{}{}
		}
	}
	return s
}

// Valid reports whether a permission name is well formed
// ("resource:action", lowercase, no spaces).
func (p Permission) Valid() bool {
	parts := strings.Split(string(p), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	return strings.ToLower(string(p)) == string(p) && !strings.ContainsAny(string(p), " \t")
}
