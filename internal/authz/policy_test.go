package authz_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/authz"
)

const (
	getPet    = "/pet.v2.PetService/GetPet"
	deletePet = "/pet.v2.PetService/DeletePet"
)

func TestPolicyAllows(t *testing.T) {
	t.Parallel()

	policy := authz.NewPolicy(map[string][]string{
		getPet:    {"viewer", "editor"},
		deletePet: {"editor"},
	})

	cases := map[string]struct {
		procedure string
		roles     []string
		want      bool
	}{
		"a listed role is allowed":                   {getPet, []string{"viewer"}, true},
		"any one matching role suffices":             {getPet, []string{"other", "editor"}, true},
		"an unlisted role is denied":                 {getPet, []string{"guest"}, false},
		"a role listed elsewhere is denied here":     {deletePet, []string{"viewer"}, false},
		"admin bypasses the matrix":                  {deletePet, []string{"admin"}, true},
		"admin bypasses an unlisted procedure too":   {"/pet.v2.PetService/Unlisted", []string{"admin"}, true},
		"an unlisted procedure is denied by default": {"/pet.v2.PetService/Unlisted", []string{"editor"}, false},
		"no roles at all is denied":                  {getPet, nil, false},
		"an empty role string does not match":        {getPet, []string{""}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, policy.Allows(tc.procedure, tc.roles))
		})
	}
}

// TestNoRulesDenyEverything pins the safe first-install posture: an operator must
// open things deliberately.
func TestNoRulesDenyEverything(t *testing.T) {
	t.Parallel()

	policy := authz.NewPolicy(nil)

	assert.False(t, policy.Allows(getPet, []string{"user"}))
	assert.False(t, policy.Allows(deletePet, []string{"editor"}))
	assert.True(t, policy.Allows(deletePet, []string{authz.AdminRole}),
		"admin must never be locked out")
}

func TestNilPolicyDeniesEverything(t *testing.T) {
	t.Parallel()

	var policy *authz.Policy

	assert.False(t, policy.Allows(getPet, []string{"admin"}),
		"a missing policy must fail closed, even for admin")
}

func TestParseRules(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		spec string
		want map[string][]string
	}{
		"a single rule": {
			spec: getPet + "=viewer",
			want: map[string][]string{getPet: {"viewer"}},
		},
		"several roles": {
			spec: getPet + "=viewer,editor",
			want: map[string][]string{getPet: {"viewer", "editor"}},
		},
		"semicolon separated": {
			spec: getPet + "=viewer; " + deletePet + "=editor",
			want: map[string][]string{getPet: {"viewer"}, deletePet: {"editor"}},
		},
		"newline separated": {
			spec: getPet + "=viewer\n" + deletePet + "=editor",
			want: map[string][]string{getPet: {"viewer"}, deletePet: {"editor"}},
		},
		"surrounding whitespace is ignored": {
			spec: "  " + getPet + " =  viewer , editor  ",
			want: map[string][]string{getPet: {"viewer", "editor"}},
		},
		"empty spec yields no rules": {
			spec: "   ",
			want: map[string][]string{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := authz.ParseRules(tc.spec)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseRulesRejectsMalformed matters more than the happy path: a dropped rule
// either opens a procedure that should be closed or closes one that should be
// open, and neither is visible at runtime.
func TestParseRulesRejectsMalformed(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"no equals sign":        getPet,
		"no procedure":          "=viewer",
		"no roles":              getPet + "=",
		"only blank roles":      getPet + "= , ",
		"procedure without a /": "pet.v2.PetService/GetPet=viewer",
	}

	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := authz.ParseRules(spec)

			require.Error(t, err, "a malformed rule must fail loudly, not be skipped")
		})
	}
}

func TestPolicyProceduresAreSorted(t *testing.T) {
	t.Parallel()

	policy := authz.NewPolicy(map[string][]string{
		deletePet: {"editor"},
		getPet:    {"viewer"},
	})

	assert.Equal(t, []string{deletePet, getPet}, policy.Procedures())
}
