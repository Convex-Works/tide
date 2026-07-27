package auth

import (
	"slices"
	"testing"
)

func TestGroupAccess(t *testing.T) {
	tests := []struct {
		name        string
		groups      []string
		userGroups  []string
		adminGroups []string
		allowed     bool
		admin       bool
	}{
		{name: "unrestricted without configured user groups", allowed: true},
		{
			name: "ordinary user", groups: []string{"klisi-users"},
			userGroups: []string{"klisi-users"}, adminGroups: []string{"admins"},
			allowed: true,
		},
		{
			name: "admin is always allowed", groups: []string{"admins"},
			userGroups: []string{"klisi-users"}, adminGroups: []string{"admins", "klisi-admins"},
			allowed: true, admin: true,
		},
		{
			name: "unrelated group denied", groups: []string{"other"},
			userGroups: []string{"klisi-users"}, adminGroups: []string{"admins"},
		},
		{
			name: "matching is case sensitive", groups: []string{"Admins"},
			userGroups: []string{"klisi-users"}, adminGroups: []string{"admins"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allowed, admin := groupAccess(test.groups, test.userGroups, test.adminGroups)
			if allowed != test.allowed || admin != test.admin {
				t.Fatalf("groupAccess() = (%t, %t), want (%t, %t)", allowed, admin, test.allowed, test.admin)
			}
		})
	}
}

func TestOIDCRequestsGroupsScope(t *testing.T) {
	for _, want := range []string{"openid", "profile", "email", "groups"} {
		if !slices.Contains(oidcScopes(), want) {
			t.Errorf("missing scope %q", want)
		}
	}
}
