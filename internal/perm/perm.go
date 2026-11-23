// Package perm is what a member may do in a space. Routes ask for a permission;
// a role is only a named set of them. This table is the one place that knows
// which role holds what, so a custom role later is one more row here and
// nothing else compares role names. (The two rules that mean the owner role
// itself, a space always keeping one and the Git author of its commits, name it
// through Owner.)
package perm

// Perm is one thing a member may be allowed to do.
type Perm uint8

const (
	// Read: see pages, history, attachments and search.
	Read Perm = 1 << iota
	// Write: create, change, move and delete pages and folders; tags,
	// attachments, imports and drafts.
	Write
	// Members: add, change and remove the members of the space.
	Members
	// Manage: rename the space, its inbox, its remote and sync, rebuild the index.
	Manage
)

var names = []struct {
	p    Perm
	name string
}{{Read, "read"}, {Write, "write"}, {Members, "members"}, {Manage, "manage"}}

func (p Perm) String() string {
	for _, n := range names {
		if n.p == p {
			return n.name
		}
	}
	return "unknown"
}

// Set is a set of permissions. It is one byte, so a Set is compared with ==,
// copied for free, and checked with one AND: nothing is looked up per request.
type Set uint8

// All is every permission.
const All = Set(Read | Write | Members | Manage)

// Of builds a set.
func Of(ps ...Perm) Set {
	var s Set
	for _, p := range ps {
		s |= Set(p)
	}
	return s
}

// Has reports whether the set holds every one of ps.
func (s Set) Has(ps ...Perm) bool {
	w := Of(ps...)
	return s&w == w
}

// Names lists the permissions of the set in a fixed order, as the API says them.
func (s Set) Names() []string {
	out := []string{}
	for _, n := range names {
		if s.Has(n.p) {
			out = append(out, n.name)
		}
	}
	return out
}

// The built-in roles.
const (
	Owner  = "owner"
	Editor = "editor"
	Viewer = "viewer"
)

// roles is every role and what it holds, in the order they are offered.
var roles = []struct {
	name string
	set  Set
}{
	{Owner, All},
	{Editor, Of(Read, Write)},
	{Viewer, Of(Read)},
}

// ForRole returns the permissions of a role, and false for a name that is not one.
func ForRole(role string) (Set, bool) {
	for _, r := range roles {
		if r.name == role {
			return r.set, true
		}
	}
	return 0, false
}

// ValidRole reports whether role is a role that can be given.
func ValidRole(role string) bool {
	_, ok := ForRole(role)
	return ok
}

// RoleNames lists the roles in the order they are offered.
func RoleNames() []string {
	n := make([]string, len(roles))
	for i, r := range roles {
		n[i] = r.name
	}
	return n
}

// RoleFor returns the built-in role whose permissions are exactly s, and ""
// when none is (nothing, or a set no built-in role holds). The union of a
// person's direct role and their groups' roles is a Set; this names it.
func RoleFor(s Set) string {
	for _, r := range roles {
		if r.set == s {
			return r.name
		}
	}
	return ""
}
