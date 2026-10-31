package approvals

// Action identifiers are authorization/approval contracts. Entries for later
// domains do not execute or imply implementation of those business workflows.
const (
	ActionInventoryRestructure        = "INVENTORY_RESTRUCTURE"
	ActionInventoryMapping            = "INVENTORY_MAPPING_CHANGE"
	ActionPayoutDestinationChange     = "BUSINESS_PAYOUT_DESTINATION_CHANGE"
	ActionHighValueRefund             = "HIGH_VALUE_REFUND"
	ActionVerifiedPropertyPublication = "VERIFIED_PROPERTY_PUBLICATION"
	ActionExceptionalRateChange       = "EXCEPTIONAL_RATE_CHANGE"
	ActionInventoryConflictOverride   = "INVENTORY_CONFLICT_OVERRIDE"
	ActionDeliberateOverbooking       = "DELIBERATE_OVERBOOKING"
	ActionExceptionalCheckIn          = "EXCEPTIONAL_CHECK_IN"
	ActionNightAuditExceptionClosure  = "NIGHT_AUDIT_EXCEPTION_CLOSURE"
	ActionExternalManagerAccess       = "EXTERNAL_MANAGER_ACCESS"
	ActionProfessionalDataRoomGrant   = "PROFESSIONAL_DATA_ROOM_GRANT"
	ActionStaffRoleChange             = "STAFF_ROLE_CHANGE"
	ActionStaffScopeChange            = "STAFF_SCOPE_CHANGE"
	ActionBreakGlassAccess            = "BREAK_GLASS_ACCESS"
	ActionBulkDisposalOrSale          = "HIGH_VALUE_BULK_DISPOSAL_OR_SALE"
)
