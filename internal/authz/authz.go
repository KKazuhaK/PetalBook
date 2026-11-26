// Package authz is who may do what outside the content of a space: a registry
// of permission keys, sets of them, the subject a request is made by, and the
// built-in roles (docs/design/auth-and-permissions.md §16).
//
// A key is registered once, at init, with its scope (site, tenant or space),
// the capability of a tenant it needs (if any) and a description. A key's bit
// exists only in this process: anything stored or sent names the key. A Set is
// two words, so asking whether a set holds a key is one AND on memory the
// session cache already holds; nothing is looked up per request.
//
// The package is server-only: the engine (internal/space and what it uses)
// never imports it (internal/boundary).
package authz

import (
	"fmt"
	"math/bits"
	"regexp"
	"sync"
)

// Scope is where a key applies.
type Scope uint8

const (
	// ScopeSite: the whole server (its settings, the tenants themselves).
	ScopeSite Scope = iota + 1
	// ScopeTenant: one tenant (its people, groups, spaces, settings).
	ScopeTenant
	// ScopeSpace: one space. Space keys name the four permissions of
	// internal/perm, which stays the bitset of the space level.
	ScopeSpace
)

func (s Scope) String() string {
	switch s {
	case ScopeSite:
		return "site"
	case ScopeTenant:
		return "tenant"
	case ScopeSpace:
		return "space"
	}
	return "unknown"
}

// maxKeys is how many keys a Set holds.
const maxKeys = 128

// Key is a registered permission key. The zero Key is none.
type Key uint8

// Info is what the registry knows of a key.
type Info struct {
	Name  string
	Scope Scope
	// Capability is the tenant capability the key needs ("" for none): with
	// it off, the key is refused in that tenant whoever holds it, except to a
	// holder of the key across the site.
	Capability  string
	Description string
}

var (
	mu       sync.Mutex
	registry []Info // registry[k-1] is key k
	byName   = map[string]Key{}
	// aliases are the former names of renamed keys: a stored role that names
	// a key by an old name still holds it.
	aliases = map[string]Key{}
	frozen  bool
)

var namePattern = regexp.MustCompile(`^(site|tenant|space)(\.[a-z][a-zA-Z]*)+$`)

// Register adds a key. It is called at init and panics on a duplicate name, a
// name that does not begin with its scope, or once the built-in roles have
// been computed (a key registered later would be missing from them).
func Register(name string, scope Scope, capability, desc string) Key {
	mu.Lock()
	defer mu.Unlock()
	if frozen {
		panic("authz: " + name + " registered after the roles were built; register keys at init")
	}
	if _, dup := byName[name]; dup {
		panic("authz: duplicate key " + name)
	}
	if _, dup := aliases[name]; dup {
		panic("authz: " + name + " is the former name of another key")
	}
	if !namePattern.MatchString(name) || name[:len(scope.String())] != scope.String() {
		panic(fmt.Sprintf("authz: key %q must begin with its scope %q", name, scope))
	}
	if desc == "" {
		panic("authz: key " + name + " has no description")
	}
	if len(registry) >= maxKeys {
		panic("authz: too many keys")
	}
	registry = append(registry, Info{Name: name, Scope: scope, Capability: capability, Description: desc})
	k := Key(len(registry))
	byName[name] = k
	return k
}

func (k Key) info() Info {
	if k == 0 || int(k) > len(registry) {
		panic("authz: not a registered key")
	}
	return registry[k-1]
}

// Name is the key's name, as stored and sent.
func (k Key) Name() string { return k.info().Name }

// Scope is where the key applies.
func (k Key) Scope() Scope { return k.info().Scope }

// Capability is the capability the key needs, "" for none.
func (k Key) Capability() string { return k.info().Capability }

func (k Key) String() string { return k.Name() }

// Alias keeps a key's former name working: when a key is renamed, its old
// name is registered here (at init, after the key), so that a stored role that
// names it still holds the key. What is sent always names the key's current
// name. It panics on a name that is a key's or already an alias, and on a key
// that is not registered.
func Alias(old string, k Key) {
	mu.Lock()
	defer mu.Unlock()
	if k == 0 || int(k) > len(registry) {
		panic("authz: alias " + old + " of a key that is not registered")
	}
	if _, dup := byName[old]; dup {
		panic("authz: alias " + old + " is the name of a key")
	}
	if _, dup := aliases[old]; dup {
		panic("authz: duplicate alias " + old)
	}
	aliases[old] = k
}

// Lookup finds a key by its name, or by a former name (see Alias).
func Lookup(name string) (Key, bool) {
	mu.Lock()
	defer mu.Unlock()
	if k, ok := byName[name]; ok {
		return k, true
	}
	k, ok := aliases[name]
	return k, ok
}

// SetOf is the set of the keys named, by their names or former names, as a
// stored role lists them. unknown is the names that are no key (one removed
// since): they hold nothing.
func SetOf(names []string) (s Set, unknown []string) {
	for _, n := range names {
		if k, ok := Lookup(n); ok {
			s = s.With(k)
		} else {
			unknown = append(unknown, n)
		}
	}
	return s, unknown
}

// All lists every key in the order it was registered.
func All() []Info {
	mu.Lock()
	defer mu.Unlock()
	return append([]Info(nil), registry...)
}

// Set is a set of keys.
type Set [2]uint64

// Of builds a set.
func Of(keys ...Key) Set {
	var s Set
	for _, k := range keys {
		s = s.With(k)
	}
	return s
}

// With returns the set with k added.
func (s Set) With(k Key) Set {
	b := uint(k) - 1
	s[b>>6] |= 1 << (b & 63)
	return s
}

// Has reports whether the set holds k: one AND.
func (s Set) Has(k Key) bool {
	if k == 0 {
		return false
	}
	b := uint(k) - 1
	return s[b>>6]&(1<<(b&63)) != 0
}

// Union is every key of either set.
func (s Set) Union(o Set) Set { return Set{s[0] | o[0], s[1] | o[1]} }

// Intersect is every key of both sets.
func (s Set) Intersect(o Set) Set { return Set{s[0] & o[0], s[1] & o[1]} }

// Empty reports whether the set holds nothing.
func (s Set) Empty() bool { return s[0]|s[1] == 0 }

// Keys lists the keys of the set in the order they were registered.
func (s Set) Keys() []Key {
	var out []Key
	for w := range s {
		for v := s[w]; v != 0; v &= v - 1 {
			out = append(out, Key(w*64+bits.TrailingZeros64(v)+1))
		}
	}
	return out
}

// Names lists the names of the set's keys in the order they were registered.
func (s Set) Names() []string {
	out := []string{}
	for _, k := range s.Keys() {
		out = append(out, k.Name())
	}
	return out
}

// OfScope is every key of a scope.
func OfScope(sc Scope) Set {
	mu.Lock()
	defer mu.Unlock()
	var s Set
	for i, in := range registry {
		if in.Scope == sc {
			s = s.With(Key(i + 1))
		}
	}
	return s
}
