package auth

import "testing"

// Audits are a tock-owned feature: the permission is not in upstream's
// PermissionStore matrix, so pin down who gets it.
func TestAuditViewPermission(t *testing.T) {
	for role, want := range map[string]bool{
		"owner":    true,
		"admin":    true,
		"manager":  false,
		"employee": false,
	} {
		got := false
		for _, permission := range rolePermissions[role] {
			if permission == "audits:view" {
				got = true
				break
			}
		}
		if got != want {
			t.Errorf("role %s audits:view = %v, want %v", role, got, want)
		}
	}
}
