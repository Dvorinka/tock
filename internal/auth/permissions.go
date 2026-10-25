package auth

// Role permission matrix ported verbatim from app/Service/PermissionStore.php.
var rolePermissions = map[string][]string{
	"owner": {
		"charts:view:own", "charts:view:all",
		"projects:view", "projects:view:all", "projects:create", "projects:update", "projects:delete",
		"project-members:view", "project-members:create", "project-members:update", "project-members:delete",
		"tasks:view", "tasks:view:all", "tasks:create", "tasks:create:all", "tasks:update", "tasks:update:all", "tasks:delete", "tasks:delete:all",
		"time-entries:view:all", "time-entries:create:all", "time-entries:update:all", "time-entries:delete:all",
		"time-entries:view:own", "time-entries:create:own", "time-entries:update:own", "time-entries:delete:own",
		"tags:view", "tags:create", "tags:update", "tags:delete",
		"clients:view", "clients:view:all", "clients:create", "clients:update", "clients:delete",
		"organizations:view", "organizations:update", "organizations:delete",
		"import", "export",
		"invitations:view", "invitations:create", "invitations:resend", "invitations:remove",
		"members:view", "members:invite-placeholder", "members:change-ownership", "members:make-placeholder", "members:merge-into", "members:update", "members:delete",
		"billing",
		"reports:view", "reports:create", "reports:update", "reports:delete",
		"goals:view:own", "goals:create:own", "goals:update:own", "goals:delete:own",
		"goals:view:organization-type", "goals:create:organization-type", "goals:update:organization-type", "goals:delete:organization-type",
		"invoices:view", "invoices:create", "invoices:update", "invoices:download", "invoices:delete",
		"invoice-recipients:view", "invoice-recipients:create", "invoice-recipients:update", "invoice-recipients:delete",
		"invoice-settings:view", "invoice-settings:update",
	},
	"admin": {
		"charts:view:own", "charts:view:all",
		"projects:view", "projects:view:all", "projects:create", "projects:update", "projects:delete",
		"project-members:view", "project-members:create", "project-members:update", "project-members:delete",
		"tasks:view", "tasks:view:all", "tasks:create", "tasks:create:all", "tasks:update", "tasks:update:all", "tasks:delete", "tasks:delete:all",
		"time-entries:view:all", "time-entries:create:all", "time-entries:update:all", "time-entries:delete:all",
		"time-entries:view:own", "time-entries:create:own", "time-entries:update:own", "time-entries:delete:own",
		"tags:view", "tags:create", "tags:update", "tags:delete",
		"clients:view", "clients:view:all", "clients:create", "clients:update", "clients:delete",
		"organizations:view", "organizations:update",
		"import", "export",
		"invitations:view", "invitations:create", "invitations:resend", "invitations:remove",
		"members:view", "members:invite-placeholder", "members:make-placeholder", "members:merge-into", "members:delete", "members:update",
		"reports:view", "reports:create", "reports:update", "reports:delete",
		"goals:view:own", "goals:create:own", "goals:update:own", "goals:delete:own",
		"goals:view:organization-type", "goals:create:organization-type", "goals:update:organization-type", "goals:delete:organization-type",
		"invoices:view", "invoices:create", "invoices:update", "invoices:download", "invoices:delete",
		"invoice-recipients:view", "invoice-recipients:create", "invoice-recipients:update", "invoice-recipients:delete",
		"invoice-settings:view", "invoice-settings:update",
	},
	"manager": {
		"charts:view:own", "charts:view:all",
		"projects:view", "projects:view:all", "projects:create", "projects:update", "projects:delete",
		"project-members:view", "project-members:create", "project-members:update", "project-members:delete",
		"tasks:view", "tasks:view:all", "tasks:create", "tasks:create:all", "tasks:update", "tasks:update:all", "tasks:delete", "tasks:delete:all",
		"time-entries:view:all", "time-entries:create:all", "time-entries:update:all", "time-entries:delete:all",
		"time-entries:view:own", "time-entries:create:own", "time-entries:update:own", "time-entries:delete:own",
		"tags:view", "tags:create", "tags:update", "tags:delete",
		"clients:view", "clients:view:all", "clients:create", "clients:update", "clients:delete",
		"organizations:view",
		"invitations:view",
		"members:view",
		"reports:view", "reports:create", "reports:update", "reports:delete",
		"goals:view:own", "goals:create:own", "goals:update:own", "goals:delete:own",
		"goals:view:organization-type", "goals:create:organization-type", "goals:update:organization-type", "goals:delete:organization-type",
		"invoices:view", "invoices:create", "invoices:update", "invoices:download", "invoices:delete",
		"invoice-recipients:view", "invoice-recipients:create", "invoice-recipients:update", "invoice-recipients:delete",
		"invoice-settings:view", "invoice-settings:update",
	},
	"employee": {
		"charts:view:own",
		"projects:view",
		"tags:view",
		"tasks:view",
		"clients:view",
		"time-entries:view:own", "time-entries:create:own", "time-entries:update:own", "time-entries:delete:own",
		"organizations:view",
		"goals:view:own", "goals:create:own", "goals:update:own", "goals:delete:own",
	},
	"placeholder": {},
}

// employeeTaskPerms are granted when organizations.employees_can_manage_tasks is set.
var employeeTaskPerms = []string{"tasks:create", "tasks:update", "tasks:delete"}

// PermissionsForRole returns the permission list for a member role.
func PermissionsForRole(role string, employeesCanManageTasks bool) []string {
	perms, ok := rolePermissions[role]
	if !ok {
		return []string{}
	}
	out := make([]string, len(perms))
	copy(out, perms)
	if role == "employee" && employeesCanManageTasks {
		out = append(out, employeeTaskPerms...)
	}
	return out
}

// RoleDefinition mirrors PermissionStore::ROLE_DEFINITIONS entries.
type RoleDefinition struct {
	Key         string
	Name        string
	Description string
}

// RoleDefinitions returns the role definitions in upstream order
// (PermissionStore::roleDefinitions() → collect → map → values()).
func RoleDefinitions() []RoleDefinition {
	return []RoleDefinition{
		{Key: "owner", Name: "Owner", Description: "Owner users can perform any action. There is only one owner per organization."},
		{Key: "admin", Name: "Administrator", Description: "Administrator users can perform any action, except accessing the billing dashboard."},
		{Key: "manager", Name: "Manager", Description: "Managers have full access to all projects, time entries, ect. but cannot manage the organization (add/remove member, edit the organization, ect.)."}, //nolint:misspell // verbatim upstream typo
		{Key: "employee", Name: "Employee", Description: "Employees have the ability to read, create, and update their own time entries, they can see the projects that they are members of and the clients they are assigned to."},
		{Key: "placeholder", Name: "Placeholder", Description: "Placeholders are used for importing data. They cannot log in and have no permissions."},
	}
}
