// Package authz decides whether a caller's roles permit an RPC.
//
// This is *action* authorization — may this role invoke DeletePet at all — not row
// filtering. Row-level rules belong in SQL WHERE clauses, where the database
// enforces them and application code cannot leak past them; the two compose.
//
// The model follows the conventional petstore shape: a declarative
// role × action matrix, deny by default, with an admin bypass. Declarative
// matters — a hardcoded role check is invisible to whoever operates the service
// and needs a code change to adjust.
package authz

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// AdminRole may invoke anything, so an operator cannot lock themselves out.
const AdminRole = "admin"

// Policy maps an RPC procedure onto the roles allowed to invoke it. The zero
// configuration — no rules at all — denies everything but admin, which is the safe
// first-install posture.
type Policy struct {
	// allowed is procedure -> set of roles. A procedure absent from the map is
	// denied, which is what makes the default safe.
	allowed map[string]map[string]bool
}

// NewPolicy builds a Policy from procedure -> roles.
//
// Requires: procedures are full Connect paths, e.g. "/pet.v2.PetService/GetPet".
// Ensures:  the Policy is immutable and safe for concurrent use.
func NewPolicy(rules map[string][]string) *Policy {
	allowed := make(map[string]map[string]bool, len(rules))
	for procedure, roles := range rules {
		set := make(map[string]bool, len(roles))
		for _, role := range roles {
			if role = strings.TrimSpace(role); role != "" {
				set[role] = true
			}
		}
		allowed[procedure] = set
	}
	return &Policy{allowed: allowed}
}

// Allows reports whether any of roles may invoke procedure.
//
// Deny by default: an unlisted procedure is refused rather than permitted, so
// adding an RPC cannot silently open it.
func (p *Policy) Allows(procedure string, roles []string) bool {
	if p == nil {
		return false
	}
	if slices.Contains(roles, AdminRole) {
		return true
	}
	permitted, known := p.allowed[procedure]
	if !known {
		return false
	}
	for _, role := range roles {
		if permitted[role] {
			return true
		}
	}
	return false
}

// Procedures lists the procedures the policy names, sorted, for startup logging.
func (p *Policy) Procedures() []string {
	if p == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(p.allowed))
}

// ParseRules reads a policy from its configured form: procedure=role[,role]
// entries separated by semicolons or newlines.
//
//	/pet.v2.PetService/GetPet=viewer,editor; /pet.v2.PetService/DeletePet=editor
//
// A malformed entry is an error rather than a skipped line: silently dropping a
// rule would either open a procedure that should be closed or close one that
// should be open, and neither failure is visible at runtime.
func ParseRules(spec string) (map[string][]string, error) {
	rules := map[string][]string{}
	for entry := range strings.FieldsFuncSeq(spec, func(r rune) bool {
		return r == ';' || r == '\n'
	}) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		procedure, roles, found := strings.Cut(entry, "=")
		procedure = strings.TrimSpace(procedure)
		if !found || procedure == "" {
			return nil, fmt.Errorf("authz: %q is not procedure=role[,role]", entry)
		}
		if !strings.HasPrefix(procedure, "/") {
			return nil, fmt.Errorf("authz: procedure %q must start with /", procedure)
		}
		parsed := parseRoles(roles)
		if len(parsed) == 0 {
			return nil, fmt.Errorf("authz: procedure %q names no roles", procedure)
		}
		rules[procedure] = parsed
	}
	return rules, nil
}

// parseRoles splits a comma-separated role list, discarding blanks.
func parseRoles(roles string) []string {
	var parsed []string
	for role := range strings.SplitSeq(roles, ",") {
		if role = strings.TrimSpace(role); role != "" {
			parsed = append(parsed, role)
		}
	}
	return parsed
}
