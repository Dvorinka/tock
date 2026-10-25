// Package domain ports the Laravel service layer (MemberService,
// OrganizationService, InvitationService, DeletionService,
// BillableRateService) to explicit sqlc calls. Keys and messages mirror
// lang/en/exceptions.php — the SPA decodes errors by key.
package domain

// APIError mirrors App\Exceptions\Api\ApiException: rendered as
// {error: true, key, message} with HTTP 400.
type APIError struct {
	Key     string
	Message string
}

func (e *APIError) Error() string { return e.Message }

var entityNames = map[string]string{
	"organization":   "organization",
	"project":        "project",
	"task":           "task",
	"time_entry":     "time entry",
	"user":           "user",
	"client":         "client",
	"member":         "member",
	"project_member": "project member",
	"tag":            "tag",
}

func apiErr(key, message string) *APIError { return &APIError{Key: key, Message: message} }

func ErrEntityStillInUse(modelToDelete, modelInUse string) *APIError {
	return apiErr("entity_still_in_use",
		"The "+entityNames[modelToDelete]+" is still used by a "+entityNames[modelInUse]+" and can not be deleted.")
}

var (
	ErrCanNotRemoveOwner           = apiErr("can_not_remove_owner_from_organization", "Can not remove owner from organization")
	ErrChangingRoleOfPlaceholder   = apiErr("changing_role_of_placeholder_is_not_allowed", "Changing role of placeholder is not allowed")
	ErrChangingRoleToPlaceholder   = apiErr("changing_role_to_placeholder_is_not_allowed", "Changing role to placeholder is not allowed")
	ErrOnlyOwnerCanChangeOwnership = apiErr("only_owner_can_change_ownership", "Only owner can change ownership")
	ErrOnlyPlaceholdersCanBeMerged = apiErr("only_placeholders_can_be_merged_into_another_member", "Only placeholders can be merged into another member")
	ErrOrganizationNeedsOwner      = apiErr("organization_needs_at_least_one_owner", "Organization needs at least one owner")
	ErrPlaceholderNotInvitable     = apiErr("this_placeholder_can_not_be_invited_use_the_merge_tool_instead_api_exception", "This placeholder can not be invited use the merge tool instead")
	ErrUserAlreadyMember           = apiErr("user_is_already_member_of_organization", "User is already a member of the organization")
	ErrUserNotPlaceholder          = apiErr("user_not_placeholder", "The given user is not a placeholder")
	ErrInvitationAlreadyExists     = apiErr("invitation_for_the_email_already_exists", "The email has already been invited to the organization. Please wait for the user to accept the invitation or resend the invitation email.")
	ErrOwnerWithMultipleMembers    = apiErr("can_not_delete_user_who_is_owner_of_organization_with_multiple_members", "Can not delete user who is owner of organization with multiple members. Please delete the organization first.")
	ErrOverlappingTimeEntry        = apiErr("overlapping_time_entry", "Overlapping time entries are not allowed.")
	ErrTimeEntryStillRunning       = apiErr("time_entry_still_running", "Time entry is still running")
	ErrTimeEntryCanNotBeRestarted  = apiErr("time_entry_can_not_be_restarted", "Time entry is already stopped and can not be restarted")
	ErrTimeEntryMemberWhileRunning = apiErr("time_entry_member_can_not_be_changed_while_running", "The member of a running time entry can not be changed")
	ErrInactiveUser                = apiErr("inactive_user_can_not_be_used", "Inactive user can not be used")
	ErrUserAlreadyProjectMember    = apiErr("user_is_already_member_of_project", "User is already a member of the project")
)

var ErrPDFRendererNotConfigured = apiErr("pdf_renderer_is_not_configured", "PDF renderer is not configured")
