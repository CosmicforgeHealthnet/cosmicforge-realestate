"""Section 16 governed operational domains: one declarative source for the
migration (tables + database-enforced state machines) and the Go domain
definitions. Run: python scripts/contracts/business_ops_domains.py [--check]
"""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICE = ROOT / 'services/business-operations-service'

# transition: action -> (from_states, to_state, capability or None (domain write), mfa, approval, evidence, distinct)
# approval: None | ('COMMERCIAL', action, approve_capability, bind_amount) | ('PRIVILEGE', approvals constant name)
def T(frm, to, cap=None, mfa=False, approval=None, evidence=False, distinct=False, assignee=False, amount=False, reference=False):
    return dict(frm=frm, to=to, cap=cap, mfa=mfa, approval=approval, evidence=evidence, distinct=distinct, assignee=assignee, amount=amount, reference=reference)

DOMAINS = [
 dict(key='housekeeping_task', table='housekeeping_tasks', path='/housekeeping/tasks', scope='PROPERTY', read='housekeeping:read', write='housekeeping:write',
  kinds=['TURNOVER', 'STAYOVER', 'DEEP_CLEAN', 'INSPECTION_ONLY', 'REWORK'], initial='PLANNED', event='housekeeping.task.updated.v1', subject='ROOM', subject_required=True,
  unique_live=['PLANNED', 'ASSIGNED', 'IN_PROGRESS', 'INSPECTION', 'REWORK'], patch=True,
  transitions={'ASSIGN': T(['PLANNED', 'ASSIGNED', 'REWORK'], 'ASSIGNED', assignee=True), 'START': T(['ASSIGNED', 'REWORK'], 'IN_PROGRESS'),
   'SUBMIT_INSPECTION': T(['IN_PROGRESS'], 'INSPECTION', evidence=True), 'PASS': T(['INSPECTION'], 'DONE', cap='room_status:inspect', distinct=True),
   'FAIL': T(['INSPECTION'], 'REWORK', cap='room_status:inspect', distinct=True, evidence=True), 'CANCEL': T(['PLANNED', 'ASSIGNED'], 'CANCELLED')}),
 dict(key='maintenance_request', table='maintenance_requests', path='/maintenance', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['CORRECTIVE', 'GUEST_REQUEST', 'SAFETY', 'INSPECTION_FINDING', 'QUALITY_ISSUE'], initial='OPEN', event='maintenance.request.updated.v1', subject='ASSET_OR_SPACE',
  transitions={'TRIAGE': T(['OPEN'], 'TRIAGED'), 'LINK_WORK_ORDER': T(['OPEN', 'TRIAGED'], 'WORK_ORDER_CREATED', reference=True), 'CLOSE': T(['TRIAGED', 'WORK_ORDER_CREATED'], 'CLOSED', evidence=True), 'REJECT': T(['OPEN', 'TRIAGED'], 'REJECTED')}),
 dict(key='work_order', table='work_orders', path='/work-orders', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['REPAIR', 'REPLACEMENT', 'SERVICE', 'PREVENTIVE', 'REWORK', 'WARRANTY'], initial='DRAFT', event='maintenance.work_order.updated.v1', subject='ASSET_OR_SPACE', approve_route=True,
  transitions={'REQUEST_QUOTE': T(['DRAFT'], 'QUOTE_REQUESTED', reference=True), 'RECORD_QUOTE': T(['QUOTE_REQUESTED'], 'QUOTED', amount=True, reference=True, evidence=True),
   'APPROVE': T(['DRAFT', 'QUOTED'], 'APPROVED', cap='maintenance:approve', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_WORK_ORDER', 'maintenance:approve', True)),
   'ASSIGN': T(['APPROVED'], 'ASSIGNED', reference=True), 'START': T(['ASSIGNED', 'REWORK'], 'IN_PROGRESS'), 'COMPLETE': T(['IN_PROGRESS'], 'COMPLETED', evidence=True),
   'VERIFY': T(['COMPLETED'], 'VERIFIED', distinct=True, evidence=True), 'REWORK': T(['COMPLETED', 'VERIFIED'], 'REWORK', evidence=True),
   'CANCEL': T(['DRAFT', 'QUOTE_REQUESTED', 'QUOTED', 'APPROVED', 'ASSIGNED'], 'CANCELLED')}),
 dict(key='preventive_schedule', table='preventive_schedules', path='/preventive-maintenance', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['INSPECTION', 'SERVICE', 'CALIBRATION', 'CLEANING', 'SAFETY_TEST'], initial='ACTIVE', event='maintenance.request.updated.v1', subject='ASSET_OR_SPACE', subject_required=True, starts_required=True,
  transitions={'PAUSE': T(['ACTIVE'], 'PAUSED'), 'RESUME': T(['PAUSED'], 'ACTIVE'), 'RETIRE': T(['ACTIVE', 'PAUSED'], 'RETIRED')}),
 dict(key='manager_assignment', table='manager_assignment_refs', path='/manager-assignments', scope='PROPERTY', read='managers:read', write='managers:write',
  kinds=['PROPERTY_MANAGER', 'MANAGEMENT_COMPANY', 'LETTING_AGENT', 'FACILITY_MANAGER'], initial='PROPOSED', event='manager.assignment.updated.v1', counterparty_required=True, ends_required=True, patch=True, revoke_route=True,
  transitions={'ACTIVATE': T(['PROPOSED'], 'ACTIVE', mfa=True, approval=('PRIVILEGE', 'ActionExternalManagerAccess'), evidence=True), 'SUSPEND': T(['ACTIVE', 'EXPIRING'], 'SUSPENDED'),
   'RESUME': T(['SUSPENDED'], 'ACTIVE', mfa=True), 'REVOKE': T(['PROPOSED', 'ACTIVE', 'SUSPENDED', 'EXPIRING'], 'REVOKED'), 'TERMINATE': T(['ACTIVE', 'SUSPENDED', 'EXPIRING'], 'TERMINATED', evidence=True)},
  system=['ACTIVE>EXPIRING', 'ACTIVE>EXPIRED', 'EXPIRING>EXPIRED', 'SUSPENDED>EXPIRED']),
 dict(key='data_room', table='data_rooms', path='/data-rooms', scope='PROPERTY', read='professional:read', write='professional:write',
  kinds=['SALE', 'LEASE', 'DEVELOPMENT', 'FINANCING', 'DUE_DILIGENCE'], initial='OPEN', event='data_room.updated.v1',
  transitions={'LOCK': T(['OPEN'], 'LOCKED'), 'UNLOCK': T(['LOCKED'], 'OPEN', mfa=True), 'ARCHIVE': T(['OPEN', 'LOCKED'], 'ARCHIVED')}),
 dict(key='professional_appointment', table='professional_appointments', path='/professional-appointments', scope='PROPERTY', read='professional:read', write='professional:write',
  kinds=['VALUER', 'SURVEYOR', 'LAWYER', 'INSPECTOR', 'ARCHITECT', 'ENGINEER', 'AUDITOR'], initial='REQUESTED', event='professional.appointment.updated.v1', counterparty_required=True,
  transitions={'ACCEPT': T(['REQUESTED'], 'ACCEPTED', reference=True), 'START': T(['ACCEPTED'], 'IN_PROGRESS'), 'SUBMIT_REPORT': T(['IN_PROGRESS'], 'REPORT_SUBMITTED', evidence=True),
   'COMPLETE': T(['REPORT_SUBMITTED'], 'COMPLETED', distinct=True), 'CANCEL': T(['REQUESTED', 'ACCEPTED', 'IN_PROGRESS'], 'CANCELLED')}),
 dict(key='data_room_grant', table='data_room_grants', path='/professional-appointments/{id}/data-room-grants', parent='professional_appointment', scope='PROPERTY', read='professional:read', write='data_room:grant',
  kinds=['VIEW_ONLY', 'WATERMARKED_DOWNLOAD'], initial='ACTIVE', event='data_room.grant.updated.v1', subject='DATA_ROOM', subject_required=True, ends_required=True,
  create_mfa=True, create_approval=('PRIVILEGE', 'ActionProfessionalDataRoomGrant'),
  transitions={'REVOKE': T(['ACTIVE'], 'REVOKED')}, system=['ACTIVE>EXPIRED']),
 dict(key='rental_application', table='rental_applications', path='/rental-applications', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['RESIDENTIAL_LONG_TERM', 'STUDENT', 'CORPORATE_HOUSING'], initial='SUBMITTED', event='rental.application.updated.v1', subject='UNIT', subject_required=True, counterparty_required=True,
  transitions={'SCREEN': T(['SUBMITTED'], 'SCREENING', reference=True), 'APPROVE': T(['SCREENING'], 'APPROVED', distinct=True), 'REJECT': T(['SUBMITTED', 'SCREENING'], 'REJECTED'),
   'WITHDRAW': T(['SUBMITTED', 'SCREENING', 'APPROVED'], 'WITHDRAWN')}, system=['APPROVED>CONVERTED']),
 dict(key='tenancy', table='tenancies', path='/tenancies', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['FIXED_TERM', 'PERIODIC'], initial='DRAFT', event='tenancy.updated.v1', subject='UNIT', subject_required=True, counterparty_required=True, amount_required=True, starts_required=True,
  unique_live=['ACTIVE', 'NOTICE_GIVEN'],
  transitions={'SEND_FOR_SIGNATURE': T(['DRAFT'], 'PENDING_SIGNATURE'), 'ACTIVATE': T(['PENDING_SIGNATURE'], 'ACTIVE', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_TENANCY', 'residential:write', True), evidence=True),
   'GIVE_NOTICE': T(['ACTIVE'], 'NOTICE_GIVEN', evidence=True), 'END': T(['NOTICE_GIVEN'], 'ENDED', evidence=True), 'TERMINATE': T(['ACTIVE', 'NOTICE_GIVEN'], 'TERMINATED', mfa=True, evidence=True),
   'CANCEL': T(['DRAFT', 'PENDING_SIGNATURE'], 'CANCELLED')}),
 dict(key='commercial_deal', table='commercial_deals', path='/commercial/deals', scope='PROPERTY', read='leasing:read', write='leasing:write',
  kinds=['LEASE', 'SALE', 'LICENCE'], initial='LEAD', event='commercial.deal.updated.v1', subject='UNIT', counterparty_required=True,
  transitions={'ISSUE_LOI': T(['LEAD'], 'LOI', evidence=True), 'NEGOTIATE': T(['LOI'], 'NEGOTIATION', amount=True), 'APPROVE': T(['NEGOTIATION'], 'APPROVED', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_DEAL', 'leasing:write', True)),
   'SIGN': T(['APPROVED'], 'SIGNED', evidence=True), 'ACTIVATE': T(['SIGNED'], 'ACTIVE'), 'CLOSE': T(['ACTIVE'], 'CLOSED', evidence=True), 'LOSE': T(['LEAD', 'LOI', 'NEGOTIATION', 'APPROVED'], 'LOST')}),
 dict(key='land_transaction', table='land_transactions', path='/land/transactions', scope='PARCEL', read='land:read', write='land:write',
  kinds=['LEASE', 'SALE', 'ALLOCATION', 'RENT'], initial='DRAFT', event='land.transaction.updated.v1', counterparty_required=True,
  transitions={'START_DUE_DILIGENCE': T(['DRAFT'], 'DUE_DILIGENCE', evidence=True), 'APPROVE': T(['DUE_DILIGENCE'], 'APPROVED', mfa=True, amount=True, approval=('COMMERCIAL', 'COMMERCIAL_LAND_TRANSACTION', 'land:write', True)),
   'COMPLETE': T(['APPROVED'], 'COMPLETED', evidence=True), 'CANCEL': T(['DRAFT', 'DUE_DILIGENCE', 'APPROVED'], 'CANCELLED')}),
 dict(key='sale_transaction', table='sale_transactions', path='/sales', scope='PROPERTY', read='sales:read', write='sales:write',
  kinds=['UNIT_SALE', 'BULK_DISPOSAL', 'OFF_PLAN_SALE'], initial='LISTED', event='sale.transaction.updated.v1', subject='UNIT',
  transitions={'APPROVE': T(['UNDER_OFFER'], 'APPROVED', cap='sales:approve', mfa=True, approval=('PRIVILEGE', 'ActionBulkDisposalOrSale')), 'CONTRACT': T(['APPROVED'], 'CONTRACTED', evidence=True),
   'COMPLETE': T(['CONTRACTED'], 'COMPLETED', evidence=True), 'WITHDRAW': T(['LISTED', 'UNDER_OFFER'], 'WITHDRAWN'), 'FALL_THROUGH': T(['UNDER_OFFER', 'APPROVED', 'CONTRACTED'], 'FALLEN_THROUGH')},
  system=['LISTED>UNDER_OFFER']),
 dict(key='sale_offer', table='sale_offers', path='/sales/{id}/offers', parent='sale_transaction', scope='PROPERTY', read='sales:read', write='sales:write',
  kinds=['CASH', 'MORTGAGE_BACKED', 'INSTALMENT'], initial='SUBMITTED', event='sale.offer.updated.v1', counterparty_required=True, amount_required=True, unique_live_parent=['ACCEPTED'],
  transitions={'ACCEPT': T(['SUBMITTED', 'COUNTERED'], 'ACCEPTED', cap='sales:approve', mfa=True), 'REJECT': T(['SUBMITTED', 'COUNTERED'], 'REJECTED'), 'COUNTER': T(['SUBMITTED'], 'COUNTERED', amount=True), 'WITHDRAW': T(['SUBMITTED', 'COUNTERED'], 'WITHDRAWN')}),
 dict(key='construction_milestone', table='construction_milestones', path='/projects/{id}/milestones', parent='DEVELOPMENT', scope='DEVELOPMENT', read='projects:read', write='projects:write',
  kinds=['FOUNDATION', 'STRUCTURE', 'ROOFING', 'MEP', 'FINISHING', 'HANDOVER_READY', 'OTHER'], initial='PLANNED', event='development.milestone.updated.v1', ends_required=True,
  transitions={'START': T(['PLANNED', 'DELAYED'], 'IN_PROGRESS'), 'REQUEST_CERTIFICATION': T(['IN_PROGRESS'], 'CERTIFICATION_PENDING', evidence=True),
   'CERTIFY': T(['CERTIFICATION_PENDING'], 'CERTIFIED', mfa=True, distinct=True, evidence=True), 'REJECT_CERTIFICATION': T(['CERTIFICATION_PENDING'], 'IN_PROGRESS', distinct=True), 'MARK_DELAYED': T(['PLANNED', 'IN_PROGRESS'], 'DELAYED', evidence=True)}),
 dict(key='development_allocation', table='development_allocations', path='/projects/{id}/allocations', parent='DEVELOPMENT', scope='DEVELOPMENT', read='projects:read', write='projects:write',
  kinds=['OFF_PLAN', 'COMPLETED_UNIT'], initial='HELD', event='development.allocation.updated.v1', subject='DEVELOPMENT_UNIT', subject_required=True, counterparty_required=True, amount_required=True, ends_required=True,
  unique_live=['HELD', 'RESERVED', 'ALLOCATED', 'CONTRACTED', 'HANDED_OVER'],
  transitions={'RESERVE': T(['HELD'], 'RESERVED', evidence=True), 'ALLOCATE': T(['RESERVED'], 'ALLOCATED', cap='sales:approve', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_DEVELOPMENT_ALLOCATION', 'sales:approve', True)),
   'CONTRACT': T(['ALLOCATED'], 'CONTRACTED', evidence=True), 'HAND_OVER': T(['CONTRACTED'], 'HANDED_OVER', evidence=True, distinct=True), 'RELEASE': T(['HELD', 'RESERVED'], 'RELEASED'),
   'CANCEL': T(['ALLOCATED', 'CONTRACTED'], 'CANCELLED', mfa=True, evidence=True)}, system=['AVAILABLE>HELD', 'HELD>RELEASED'], extra_states=['AVAILABLE']),
 dict(key='snag_item', table='snag_items', path='/projects/{id}/snagging', parent='DEVELOPMENT', scope='DEVELOPMENT', read='projects:read', write='projects:write',
  kinds=['FINISH', 'MEP', 'STRUCTURAL', 'SAFETY', 'COSMETIC'], initial='OPEN', event='development.snag.updated.v1', subject='DEVELOPMENT_UNIT', subject_required=True,
  transitions={'ASSIGN': T(['OPEN'], 'ASSIGNED', reference=True), 'MARK_FIXED': T(['ASSIGNED', 'OPEN'], 'FIXED', evidence=True), 'VERIFY': T(['FIXED'], 'VERIFIED', distinct=True, evidence=True),
   'REOPEN': T(['FIXED'], 'OPEN', evidence=True), 'REJECT': T(['OPEN'], 'REJECTED')}),
 dict(key='warranty_case', table='warranty_cases', path='/projects/{id}/warranties', parent='DEVELOPMENT', scope='DEVELOPMENT', read='projects:read', write='projects:write',
  kinds=['STRUCTURAL', 'MEP', 'FINISH', 'APPLIANCE', 'OTHER'], initial='OPEN', event='development.warranty.updated.v1', subject='DEVELOPMENT_UNIT', subject_required=True, counterparty_required=True,
  transitions={'ACCEPT': T(['OPEN'], 'ACCEPTED', evidence=True), 'START_REPAIR': T(['ACCEPTED'], 'IN_REPAIR', reference=True), 'RESOLVE': T(['IN_REPAIR'], 'RESOLVED', evidence=True, distinct=True), 'REJECT': T(['OPEN'], 'REJECTED', evidence=True)}),
 dict(key='risk_case', table='risk_cases', path='/risk-cases', scope='PROPERTY', read='risk:read', write='risk:write',
  kinds=['RESERVATION_RISK', 'PAYMENT_FRAUD', 'IDENTITY', 'PROPERTY_DAMAGE', 'ACCOUNT_ABUSE', 'CHANNEL_FRAUD'], initial='OPEN', event='risk.case.updated.v1',
  transitions={'REVIEW': T(['OPEN'], 'UNDER_REVIEW', assignee=True), 'PROPOSE_RESTRICTION': T(['UNDER_REVIEW'], 'RESTRICTION_PROPOSED', evidence=True),
   'APPROVE_RESTRICTION': T(['RESTRICTION_PROPOSED'], 'RESTRICTED', mfa=True, distinct=True), 'APPEAL': T(['RESTRICTED'], 'APPEALED', evidence=True),
   'UPHOLD': T(['APPEALED'], 'RESTRICTED', mfa=True, distinct=True), 'CLEAR': T(['UNDER_REVIEW', 'RESTRICTION_PROPOSED', 'RESTRICTED', 'APPEALED'], 'CLEARED', distinct=True), 'CLOSE': T(['CLEARED'], 'CLOSED')}),
 dict(key='dispute', table='disputes', path='/disputes', scope='PROPERTY', read='disputes:read', write='disputes:write',
  kinds=['PAYMENT', 'CHARGEBACK', 'DAMAGE', 'SERVICE', 'CONTRACT', 'DEPOSIT'], initial='OPEN', event='dispute.updated.v1', counterparty_required=True,
  transitions={'COLLECT_EVIDENCE': T(['OPEN'], 'EVIDENCE_COLLECTION'), 'SUBMIT': T(['EVIDENCE_COLLECTION'], 'SUBMITTED', evidence=True, reference=True), 'RECORD_DECISION': T(['SUBMITTED'], 'DECIDED', mfa=True, reference=True),
   'RESOLVE': T(['DECIDED'], 'RESOLVED', evidence=True), 'WITHDRAW': T(['OPEN', 'EVIDENCE_COLLECTION', 'SUBMITTED'], 'WITHDRAWN')}),
 dict(key='financing_offer', table='finance_offer_refs', path='/financing', scope='PROPERTY', read='financing:read', write='financing:write',
  kinds=['MORTGAGE', 'PAYMENT_PLAN', 'INSTALMENT', 'BRIDGE'], initial='REQUESTED', event='financing.status.updated.v1', counterparty_required=True, amount_required=True,
  transitions={'RECORD_CONSENT': T(['REQUESTED'], 'CONSENTED', evidence=True), 'SUBMIT': T(['CONSENTED'], 'SUBMITTED', reference=True), 'RECORD_APPROVAL': T(['SUBMITTED'], 'APPROVED', reference=True, evidence=True),
   'RECORD_DECLINE': T(['SUBMITTED'], 'DECLINED', reference=True), 'WITHDRAW': T(['REQUESTED', 'CONSENTED', 'SUBMITTED'], 'WITHDRAWN')}),
 dict(key='insurance_policy', table='insurance_policy_refs', path='/insurance/policies', scope='PROPERTY', read='insurance:read', write='insurance:write',
  kinds=['PROPERTY', 'LIABILITY', 'CONTENTS', 'BUSINESS_INTERRUPTION', 'CONSTRUCTION'], initial='ACTIVE', event='insurance.policy.updated.v1', counterparty_required=True, starts_required=True, ends_required=True,
  transitions={'CANCEL': T(['ACTIVE', 'EXPIRING'], 'CANCELLED', evidence=True)}, system=['ACTIVE>EXPIRING', 'ACTIVE>EXPIRED', 'EXPIRING>EXPIRED']),
 dict(key='insurance_claim', table='insurance_claim_refs', path='/insurance/claims', parent='insurance_policy', parent_in_body=True, scope='PROPERTY', read='insurance:read', write='insurance:write',
  kinds=['DAMAGE', 'THEFT', 'LIABILITY', 'WEATHER', 'OTHER'], initial='DRAFT', event='insurance.claim.updated.v1', amount_required=True,
  transitions={'SUBMIT': T(['DRAFT'], 'SUBMITTED', evidence=True, reference=True), 'RECORD_REVIEW': T(['SUBMITTED'], 'UNDER_REVIEW', reference=True), 'RECORD_SETTLEMENT': T(['UNDER_REVIEW'], 'SETTLED', reference=True, evidence=True),
   'RECORD_REJECTION': T(['UNDER_REVIEW'], 'REJECTED', reference=True), 'WITHDRAW': T(['DRAFT', 'SUBMITTED'], 'WITHDRAWN')}),
 dict(key='access_device', table='access_devices', path='/access/devices', scope='PROPERTY', read='pms:read', write='pms:write',
  kinds=['SMART_LOCK', 'KEYPAD', 'KEY_CABINET', 'GATE'], initial='REGISTERED', event='access.device.alert.v1', subject='ASSET_OR_SPACE', subject_required=True, counterparty_required=True,
  transitions={'ACTIVATE': T(['REGISTERED', 'OFFLINE'], 'ACTIVE', evidence=True, reference=True), 'MARK_OFFLINE': T(['ACTIVE'], 'OFFLINE'), 'RETIRE': T(['REGISTERED', 'ACTIVE', 'OFFLINE'], 'RETIRED')}),
 dict(key='access_grant', table='access_grants', path='/access/grants', parent='access_device', parent_in_body=True, scope='PROPERTY', read='pms:read', write='pms:write',
  kinds=['GUEST', 'STAFF', 'CONTRACTOR', 'MANAGER'], initial='REQUESTED', event='access.grant.updated.v1', counterparty_required=True, starts_required=True, ends_required=True, revoke_route=True,
  transitions={'ACTIVATE': T(['REQUESTED'], 'ACTIVE', evidence=True, reference=True), 'REVOKE': T(['REQUESTED', 'ACTIVE'], 'REVOKED')}, system=['REQUESTED>EXPIRED', 'ACTIVE>EXPIRED']),
 dict(key='integration_client', table='integration_clients', path='/integrations', scope='COMPANY', read='integrations:read', write='integrations:write',
  kinds=['ACCOUNTING', 'POS', 'CRM_SYNC', 'PARTNER_API', 'CHANNEL_TOOL', 'DATA_EXPORT'], initial='DRAFT', event='integration.health.changed.v1', counterparty_required=True,
  transitions={'ACTIVATE': T(['DRAFT', 'SUSPENDED'], 'ACTIVE', mfa=True, evidence=True), 'SUSPEND': T(['ACTIVE'], 'SUSPENDED'), 'REVOKE': T(['DRAFT', 'ACTIVE', 'SUSPENDED'], 'REVOKED')}),
]

# ---------------------------------------------------------------------------
# Migration 00038: Annex D lifecycles previously recorded as deferred in the
# database catalogue. Additive only; 00036 above is frozen history.
# scope 'NODE' accepts a PROPERTY or PARCEL inventory node; subject
# 'RECORD:t1|t2' accepts a governed record of one of those tables in the same
# scope. expire: (from_states, to_state, timestamp column, action) sweeps run
# by the operations worker under the service identity.
REC_CONTRACT = 'RECORD:tenancies|commercial_deals'
REC_DEAL = 'RECORD:sale_transactions|land_transactions|commercial_deals'
REC_AGREEMENT = 'RECORD:tenancies|commercial_deals|sale_transactions|land_transactions'
DOMAINS_38 = [
 # D.9 Residential rental operations
 dict(key='rental_offer', table='rental_offers', path='/rental-applications/{id}/offers', parent='rental_application', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['STANDARD', 'CONDITIONAL'], initial='DRAFT', event='rental.offer.updated.v1', amount_required=True, ends_required=True, unique_live_parent=['DRAFT', 'ISSUED', 'ACCEPTED'],
  transitions={'ISSUE': T(['DRAFT'], 'ISSUED', distinct=False), 'RECORD_ACCEPTANCE': T(['ISSUED'], 'ACCEPTED', evidence=True), 'RECORD_DECLINE': T(['ISSUED'], 'DECLINED'),
   'WITHDRAW': T(['DRAFT', 'ISSUED'], 'WITHDRAWN')}, system=['ISSUED>EXPIRED', 'ACCEPTED>CONVERTED'], expire=[(['ISSUED'], 'EXPIRED', 'ends_at', 'OFFER_EXPIRED')]),
 dict(key='agreement', table='agreements', path='/agreements', scope='NODE', read='residential:read', write='residential:write',
  kinds=['RESIDENTIAL_TENANCY', 'COMMERCIAL_LEASE', 'LAND_LEASE', 'SALE_CONTRACT', 'AMENDMENT'], initial='DRAFT', event='agreement.updated.v1', subject=REC_AGREEMENT, subject_required=True, counterparty_required=True,
  unique_live=['SIGNED'],
  transitions={'ISSUE': T(['DRAFT'], 'ISSUED', evidence=True), 'RECORD_SIGNATURE': T(['ISSUED'], 'SIGNED', evidence=True, reference=True, distinct=True),
   'SUPERSEDE': T(['SIGNED'], 'SUPERSEDED', mfa=True, reference=True), 'VOID': T(['DRAFT', 'ISSUED'], 'VOIDED')}),
 dict(key='rent_obligation', table='rent_obligations', path='/rent-obligations', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['RENT', 'SERVICE_CHARGE', 'UTILITY_RECHARGE', 'LATE_FEE', 'FIT_OUT_CONTRIBUTION'], initial='SCHEDULED', event='tenancy.obligation.updated.v1', subject=REC_CONTRACT, subject_required=True,
  amount_required=True, starts_required=True, ends_required=True,
  transitions={'LINK_FINANCIAL_OBLIGATION': T(['SCHEDULED', 'DUE', 'OVERDUE'], 'LINKED', reference=True),
   'RECORD_SETTLEMENT': T(['LINKED', 'OVERDUE'], 'SETTLED'), 'WAIVE': T(['SCHEDULED', 'DUE', 'OVERDUE'], 'WAIVED', mfa=True, distinct=True, evidence=True),
   'CANCEL': T(['SCHEDULED'], 'CANCELLED')},
  system=['SCHEDULED>DUE', 'DUE>OVERDUE', 'LINKED>OVERDUE'],
  expire=[(['SCHEDULED'], 'DUE', 'starts_at', 'OBLIGATION_DUE'), (['DUE', 'LINKED'], 'OVERDUE', 'ends_at', 'OBLIGATION_OVERDUE')]),
 dict(key='deposit_obligation', table='deposit_obligations', path='/deposits', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['SECURITY_DEPOSIT', 'HOLDING_DEPOSIT', 'COMMERCIAL_DEPOSIT'], initial='REQUESTED', event='tenancy.deposit.updated.v1', subject=REC_CONTRACT, subject_required=True, amount_required=True,
  transitions={'LINK_FINANCIAL_OBLIGATION': T(['REQUESTED'], 'COLLECTION_PENDING', reference=True), 'RECORD_HELD': T(['COLLECTION_PENDING'], 'HELD'),
   'REQUEST_RELEASE': T(['HELD', 'DISPUTED'], 'RELEASE_REQUESTED', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_DEPOSIT_RELEASE', 'finance:approve', True)),
   'DISPUTE': T(['HELD', 'RELEASE_REQUESTED'], 'DISPUTED', evidence=True), 'RECORD_RELEASE': T(['RELEASE_REQUESTED'], 'RELEASED', reference=True), 'CANCEL': T(['REQUESTED'], 'CANCELLED')}),
 dict(key='notice', table='notices', path='/notices', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['TERMINATION_BY_LANDLORD', 'TERMINATION_BY_TENANT', 'RENT_REVIEW', 'BREACH', 'ENTRY', 'NON_RENEWAL'], initial='DRAFT', event='tenancy.notice.updated.v1', subject=REC_CONTRACT, subject_required=True,
  starts_required=True,
  transitions={'SERVE': T(['DRAFT'], 'SERVED', evidence=True), 'ACKNOWLEDGE': T(['SERVED'], 'ACKNOWLEDGED', evidence=True), 'DISPUTE': T(['SERVED', 'ACKNOWLEDGED'], 'DISPUTED', evidence=True),
   'WITHDRAW': T(['DRAFT', 'SERVED', 'ACKNOWLEDGED', 'DISPUTED'], 'WITHDRAWN', mfa=True)}, system=['SERVED>EFFECTIVE', 'ACKNOWLEDGED>EFFECTIVE'],
  expire=[(['SERVED', 'ACKNOWLEDGED'], 'EFFECTIVE', 'starts_at', 'NOTICE_EFFECTIVE')]),
 dict(key='renewal_case', table='renewal_cases', path='/renewals', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['RESIDENTIAL', 'COMMERCIAL'], initial='OPEN', event='tenancy.renewal.updated.v1', subject=REC_CONTRACT, subject_required=True, ends_required=True, unique_live=['OPEN', 'OFFERED', 'ACCEPTED'],
  transitions={'OFFER': T(['OPEN'], 'OFFERED', amount=True), 'RECORD_ACCEPTANCE': T(['OFFERED'], 'ACCEPTED', evidence=True), 'RECORD_DECLINE': T(['OFFERED'], 'DECLINED'),
   'COMPLETE': T(['ACCEPTED'], 'COMPLETED', reference=True), 'CANCEL': T(['OPEN', 'OFFERED'], 'CANCELLED')}, system=['OPEN>LAPSED', 'OFFERED>LAPSED'],
  expire=[(['OPEN', 'OFFERED'], 'LAPSED', 'ends_at', 'RENEWAL_LAPSED')]),
 dict(key='move_out_case', table='move_out_cases', path='/move-outs', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['END_OF_TERM', 'EARLY_TERMINATION', 'EVICTION_ORDER', 'ABANDONMENT'], initial='SCHEDULED', event='tenancy.move_out.updated.v1', subject=REC_CONTRACT, subject_required=True, starts_required=True,
  unique_live=['SCHEDULED', 'INSPECTED', 'SETTLEMENT_PROPOSED', 'DISPUTED'],
  transitions={'RECORD_INSPECTION': T(['SCHEDULED'], 'INSPECTED', evidence=True), 'PROPOSE_SETTLEMENT': T(['INSPECTED', 'DISPUTED'], 'SETTLEMENT_PROPOSED', amount=True),
   'DISPUTE': T(['SETTLEMENT_PROPOSED'], 'DISPUTED', evidence=True), 'AGREE_SETTLEMENT': T(['SETTLEMENT_PROPOSED'], 'SETTLED', distinct=True, evidence=True),
   'CLOSE': T(['SETTLED'], 'CLOSED'), 'CANCEL': T(['SCHEDULED'], 'CANCELLED')}),
 dict(key='utility_account', table='utility_accounts', path='/utility-accounts', scope='PROPERTY', read='residential:read', write='residential:write',
  kinds=['ELECTRICITY', 'WATER', 'GAS', 'INTERNET', 'WASTE', 'ESTATE_SERVICE'], initial='ACTIVE', event='tenancy.utility.updated.v1', subject='ASSET_OR_SPACE', subject_required=True, counterparty_required=True,
  unique_live=['ACTIVE', 'TRANSFER_PENDING'],
  transitions={'REQUEST_TRANSFER': T(['ACTIVE'], 'TRANSFER_PENDING', reference=True), 'RECORD_TRANSFER': T(['TRANSFER_PENDING'], 'TRANSFERRED', evidence=True), 'CANCEL_TRANSFER': T(['TRANSFER_PENDING'], 'ACTIVE'),
   'CLOSE': T(['ACTIVE'], 'CLOSED', evidence=True)}),
 # D.10 Commercial property operations
 dict(key='commercial_loi', table='commercial_lois', path='/commercial/deals/{id}/lois', parent='commercial_deal', scope='PROPERTY', read='leasing:read', write='leasing:write',
  kinds=['NON_BINDING', 'BINDING_EXCLUSIVITY'], initial='DRAFT', event='commercial.transaction.updated.v1', ends_required=True, unique_live_parent=['DRAFT', 'ISSUED', 'COUNTERSIGNED'],
  transitions={'ISSUE': T(['DRAFT'], 'ISSUED', evidence=True), 'RECORD_COUNTERSIGNATURE': T(['ISSUED'], 'COUNTERSIGNED', evidence=True), 'SUPERSEDE': T(['COUNTERSIGNED'], 'SUPERSEDED'),
   'WITHDRAW': T(['DRAFT', 'ISSUED'], 'WITHDRAWN')}, system=['ISSUED>EXPIRED'], expire=[(['ISSUED'], 'EXPIRED', 'ends_at', 'LOI_EXPIRED')]),
 dict(key='commercial_terms', table='commercial_terms', path='/commercial/deals/{id}/terms', parent='commercial_deal', scope='PROPERTY', read='leasing:read', write='leasing:write',
  kinds=['HEADS_OF_TERMS', 'RENT_REVIEW', 'FIT_OUT', 'AMENDMENT'], initial='PROPOSED', event='commercial.transaction.updated.v1', amount_required=True, starts_required=True, unique_live_parent=['APPROVED'],
  transitions={'APPROVE': T(['PROPOSED'], 'APPROVED', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_LEASE_TERMS', 'leasing:write', True)), 'REJECT': T(['PROPOSED'], 'REJECTED'),
   'SUPERSEDE': T(['APPROVED'], 'SUPERSEDED')}),
 dict(key='service_charge_budget', table='service_charge_budgets', path='/commercial/service-charge-budgets', scope='PROPERTY', read='leasing:read', write='leasing:write',
  kinds=['ANNUAL', 'SUPPLEMENTARY'], initial='DRAFT', event='commercial.service_charge.updated.v1', amount_required=True, starts_required=True, ends_required=True,
  transitions={'APPROVE': T(['DRAFT'], 'APPROVED', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_SERVICE_CHARGE_BUDGET', 'finance:approve', True)),
   'RECORD_RECONCILIATION': T(['APPROVED'], 'RECONCILED', amount=True, evidence=True, distinct=True), 'CLOSE': T(['RECONCILED'], 'CLOSED'), 'CANCEL': T(['DRAFT'], 'CANCELLED')}),
 # D.11 Land
 dict(key='land_terms', table='land_terms', path='/land/transactions/{id}/terms', parent='land_transaction', scope='PARCEL', read='land:read', write='land:write',
  kinds=['LEASE_TERMS', 'SALE_TERMS', 'RENT_TERMS', 'ALLOCATION_TERMS'], initial='PROPOSED', event='land.transaction.updated.v1', amount_required=True, unique_live_parent=['APPROVED'],
  transitions={'APPROVE': T(['PROPOSED'], 'APPROVED', mfa=True, distinct=True), 'REJECT': T(['PROPOSED'], 'REJECTED'), 'SUPERSEDE': T(['APPROVED'], 'SUPERSEDED')}),
 dict(key='land_allocation', table='land_allocations', path='/land/allocations', scope='PARCEL', read='land:read', write='land:write',
  kinds=['PLOT_ALLOCATION', 'LEASE_ALLOCATION', 'SALE_ALLOCATION'], initial='PROVISIONAL', event='land.allocation.updated.v1', counterparty_required=True, amount_required=True, ends_required=True,
  unique_live_scope=['PROVISIONAL', 'CONFIRMED', 'PERFECTED', 'DISPUTED'],
  transitions={'CONFIRM': T(['PROVISIONAL'], 'CONFIRMED', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_LAND_ALLOCATION', 'land:write', True), reference=True),
   'RECORD_PERFECTION': T(['CONFIRMED'], 'PERFECTED', evidence=True, reference=True, distinct=True), 'DISPUTE': T(['PROVISIONAL', 'CONFIRMED', 'PERFECTED'], 'DISPUTED', evidence=True),
   'RESOLVE_DISPUTE': T(['DISPUTED'], 'CONFIRMED', mfa=True, distinct=True, evidence=True), 'REVOKE': T(['PROVISIONAL', 'CONFIRMED', 'DISPUTED'], 'REVOKED', mfa=True, evidence=True),
   'RELEASE': T(['PROVISIONAL'], 'RELEASED')}, system=['PROVISIONAL>RELEASED'], expire=[(['PROVISIONAL'], 'RELEASED', 'ends_at', 'ALLOCATION_HOLD_EXPIRED')]),
 # D.11/D.12 Due diligence and closing (property or parcel scope)
 dict(key='due_diligence_item', table='due_diligence_items', path='/due-diligence', scope='NODE', read='sales:read', write='sales:write',
  kinds=['TITLE', 'SURVEY', 'PLANNING', 'ENVIRONMENTAL', 'LEGAL', 'FINANCIAL', 'TECHNICAL', 'ENCUMBRANCE'], initial='OPEN', event='sale.due_diligence.updated.v1', subject=REC_DEAL, subject_required=True,
  transitions={'START_REVIEW': T(['OPEN'], 'IN_REVIEW', reference=True), 'SATISFY': T(['IN_REVIEW'], 'SATISFIED', evidence=True, distinct=True), 'FAIL': T(['IN_REVIEW'], 'FAILED', evidence=True),
   'WAIVE': T(['OPEN', 'IN_REVIEW', 'FAILED'], 'WAIVED', mfa=True, distinct=True), 'REOPEN': T(['FAILED'], 'OPEN')}),
 dict(key='closing_milestone', table='closing_milestones', path='/closing-milestones', scope='NODE', read='sales:read', write='sales:write',
  kinds=['DEPOSIT_PAID', 'CONDITIONS_SATISFIED', 'CONTRACT_EXCHANGED', 'BALANCE_PAID', 'TRANSFER_FILED', 'KEYS_HANDED_OVER', 'REGISTRATION_PERFECTED'], initial='PENDING', event='sale.closing.updated.v1',
  subject=REC_DEAL, subject_required=True, ends_required=True,
  transitions={'SUBMIT_EVIDENCE': T(['PENDING', 'REJECTED'], 'EVIDENCED', evidence=True, reference=True), 'CONFIRM': T(['EVIDENCED'], 'CONFIRMED', distinct=True),
   'REJECT': T(['EVIDENCED'], 'REJECTED'), 'WAIVE': T(['PENDING'], 'WAIVED', mfa=True, distinct=True)}),
 # D.12 Sales unit holds
 dict(key='sale_allocation', table='sale_allocations', path='/sales/{id}/unit-holds', parent='sale_transaction', scope='PROPERTY', read='sales:read', write='sales:write',
  kinds=['BUYER_HOLD', 'RESERVATION_FEE_HOLD'], initial='HELD', event='sale.allocation.updated.v1', subject='UNIT', subject_required=True, counterparty_required=True, ends_required=True,
  unique_live=['HELD', 'CONVERTED'],
  transitions={'CONVERT': T(['HELD'], 'CONVERTED', evidence=True), 'RELEASE': T(['HELD', 'CONVERTED'], 'RELEASED')}, system=['HELD>EXPIRED'],
  expire=[(['HELD'], 'EXPIRED', 'ends_at', 'UNIT_HOLD_EXPIRED')]),
 # D.13 Development
 dict(key='progress_certification', table='progress_certifications', path='/projects/{id}/progress-certifications', parent='DEVELOPMENT', scope='DEVELOPMENT', read='projects:read', write='projects:write',
  kinds=['INTERIM', 'PRACTICAL_COMPLETION', 'FINAL'], initial='SUBMITTED', event='development.progress.updated.v1',
  transitions={'START_REVIEW': T(['SUBMITTED'], 'PROFESSIONAL_REVIEW', reference=True), 'CERTIFY': T(['PROFESSIONAL_REVIEW'], 'CERTIFIED', mfa=True, distinct=True, evidence=True, reference=True),
   'REJECT': T(['SUBMITTED', 'PROFESSIONAL_REVIEW'], 'REJECTED', evidence=True), 'WITHDRAW': T(['SUBMITTED'], 'WITHDRAWN')}),
 dict(key='project_variation', table='project_variations', path='/projects/{id}/variations', parent='DEVELOPMENT', scope='DEVELOPMENT', read='projects:read', write='projects:write',
  kinds=['SCOPE', 'SPECIFICATION', 'SCHEDULE', 'COST', 'UNIT_MIX'], initial='PROPOSED', event='development.variation.updated.v1',
  transitions={'COST': T(['PROPOSED'], 'COSTED', amount=True, evidence=True), 'APPROVE': T(['COSTED'], 'APPROVED', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_PROJECT_VARIATION', 'projects:write', True)),
   'IMPLEMENT': T(['APPROVED'], 'IMPLEMENTED', evidence=True), 'REJECT': T(['PROPOSED', 'COSTED'], 'REJECTED'), 'WITHDRAW': T(['PROPOSED', 'COSTED'], 'WITHDRAWN')}),
 # D.20 Maintenance vendors and assets
 dict(key='vendor_quote', table='vendor_quote_refs', path='/work-orders/{id}/quotes', parent='work_order', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['FIXED_PRICE', 'TIME_AND_MATERIALS', 'CALL_OUT'], initial='REQUESTED', event='maintenance.quote.updated.v1', counterparty_required=True, unique_live_parent=['ACCEPTED'],
  transitions={'RECORD_QUOTE': T(['REQUESTED'], 'RECEIVED', amount=True, reference=True, evidence=True), 'ACCEPT': T(['RECEIVED'], 'ACCEPTED', distinct=True), 'REJECT': T(['RECEIVED'], 'REJECTED'),
   'WITHDRAW': T(['REQUESTED', 'RECEIVED'], 'WITHDRAWN')}),
 dict(key='provider_assignment', table='provider_assignment_refs', path='/work-orders/{id}/provider-assignments', parent='work_order', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['ARTISAN', 'SERVICE_COMPANY', 'IN_HOUSE'], initial='REQUESTED', event='maintenance.assignment.updated.v1', counterparty_required=True, unique_live_parent=['REQUESTED', 'ACCEPTED', 'ON_SITE'],
  transitions={'RECORD_ACCEPTANCE': T(['REQUESTED'], 'ACCEPTED', reference=True), 'RECORD_ARRIVAL': T(['ACCEPTED'], 'ON_SITE', evidence=True), 'RECORD_COMPLETION': T(['ON_SITE'], 'COMPLETED', evidence=True),
   'RECORD_DECLINE': T(['REQUESTED'], 'DECLINED'), 'CANCEL': T(['REQUESTED', 'ACCEPTED'], 'CANCELLED')}),
 dict(key='warranty', table='warranties', path='/warranties', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['WORKMANSHIP', 'MANUFACTURER', 'EXTENDED', 'STRUCTURAL'], initial='ACTIVE', event='maintenance.warranty.updated.v1', subject='ASSET_OR_SPACE', subject_required=True, counterparty_required=True,
  starts_required=True, ends_required=True,
  transitions={'CLAIM': T(['ACTIVE'], 'CLAIMED', evidence=True, reference=True), 'RECORD_CLAIM_OUTCOME': T(['CLAIMED'], 'ACTIVE', evidence=True), 'VOID': T(['ACTIVE'], 'VOIDED', mfa=True, evidence=True)},
  system=['ACTIVE>EXPIRED', 'CLAIMED>EXPIRED'], expire=[(['ACTIVE'], 'EXPIRED', 'ends_at', 'WARRANTY_EXPIRED')]),
 dict(key='service_contract', table='service_contract_refs', path='/service-contracts', scope='PROPERTY', read='maintenance:read', write='maintenance:write',
  kinds=['FACILITY_MANAGEMENT', 'SECURITY', 'CLEANING', 'ELEVATOR', 'HVAC', 'PEST_CONTROL', 'WASTE', 'GENERATOR'], initial='ACTIVE', event='maintenance.service_contract.updated.v1', counterparty_required=True,
  starts_required=True, ends_required=True,
  transitions={'TERMINATE': T(['ACTIVE', 'EXPIRING'], 'TERMINATED', mfa=True, evidence=True), 'RENEW': T(['EXPIRING'], 'ACTIVE', mfa=True, evidence=True)},
  system=['ACTIVE>EXPIRING', 'ACTIVE>EXPIRED', 'EXPIRING>EXPIRED'], expire=[(['ACTIVE', 'EXPIRING'], 'EXPIRED', 'ends_at', 'CONTRACT_EXPIRED')], expiring=('ACTIVE', 'EXPIRING', 30, 'CONTRACT_EXPIRING')),
 # D.19 / D.21 Managers and professionals
 dict(key='manager_performance', table='manager_performance_refs', path='/manager-assignments/{id}/performance', parent='manager_assignment', scope='PROPERTY', read='managers:read', write='managers:write',
  kinds=['MONTHLY', 'QUARTERLY', 'ANNUAL', 'INCIDENT'], initial='DRAFT', event='manager.assignment.updated.v1', starts_required=True, ends_required=True,
  transitions={'PUBLISH': T(['DRAFT'], 'PUBLISHED', evidence=True), 'ACKNOWLEDGE': T(['PUBLISHED'], 'ACKNOWLEDGED', reference=True), 'DISPUTE': T(['PUBLISHED'], 'DISPUTED', evidence=True),
   'RESOLVE': T(['DISPUTED'], 'ACKNOWLEDGED', distinct=True)}),
 dict(key='professional_finding', table='professional_findings', path='/professional-appointments/{id}/findings', parent='professional_appointment', scope='PROPERTY', read='professional:read', write='professional:write',
  kinds=['DEFECT', 'VALUATION', 'LEGAL_ISSUE', 'COMPLIANCE', 'TITLE_ISSUE', 'OBSERVATION'], initial='RECORDED', event='professional.finding.updated.v1',
  transitions={'ACCEPT': T(['RECORDED'], 'ACCEPTED', distinct=True), 'LINK_CONDITION': T(['ACCEPTED'], 'LINKED_TO_CONDITION', reference=True), 'RESOLVE': T(['ACCEPTED', 'LINKED_TO_CONDITION'], 'RESOLVED', evidence=True),
   'DISPUTE': T(['RECORDED', 'ACCEPTED'], 'DISPUTED', evidence=True), 'REJECT': T(['RECORDED', 'DISPUTED'], 'REJECTED', distinct=True)}),
 # D.24 / D.42 Risk and protection
 dict(key='risk_signal', table='risk_signals', path='/risk/signals', scope='PROPERTY', read='risk:read', write='risk:write',
  kinds=['PAYMENT_ANOMALY', 'IDENTITY_MISMATCH', 'VELOCITY', 'CHANNEL_REPORT', 'DAMAGE_HISTORY', 'STAFF_REPORT'], initial='OBSERVED', event='risk.signal.updated.v1',
  transitions={'LINK_CASE': T(['OBSERVED'], 'LINKED', reference=True), 'DISMISS': T(['OBSERVED', 'LINKED'], 'DISMISSED', distinct=True)}, system=['OBSERVED>EXPIRED'],
  expire=[(['OBSERVED'], 'EXPIRED', 'ends_at', 'SIGNAL_EXPIRED')]),
 dict(key='protection_case', table='protection_case_refs', path='/protection-cases', parent='risk_case', parent_in_body=True, scope='PROPERTY', read='risk:read', write='risk:write',
  kinds=['INSURANCE_CLAIM', 'VERIFICATION_REVIEW', 'ADMIN_ESCALATION', 'DAMAGE_PROTECTION', 'PAYMENT_PROTECTION'], initial='REQUESTED', event='reservation.risk.changed.v1', counterparty_required=True,
  transitions={'RECORD_OPENED': T(['REQUESTED'], 'OPEN', reference=True), 'RECORD_RESOLUTION': T(['OPEN'], 'RESOLVED', reference=True, evidence=True), 'RECORD_REJECTION': T(['OPEN'], 'REJECTED', reference=True),
   'WITHDRAW': T(['REQUESTED', 'OPEN'], 'WITHDRAWN')}),
 dict(key='incident_case', table='incident_cases', path='/incidents', scope='PROPERTY', read='risk:read', write='risk:write',
  kinds=['GUEST_INJURY', 'PROPERTY_DAMAGE', 'NOISE', 'SECURITY', 'THEFT', 'SAFETY', 'NEIGHBOUR_COMPLAINT'], initial='OPEN', event='risk.incident.updated.v1',
  transitions={'INVESTIGATE': T(['OPEN'], 'INVESTIGATING', assignee=True), 'RESOLVE': T(['INVESTIGATING'], 'RESOLVED', evidence=True), 'ESCALATE': T(['OPEN', 'INVESTIGATING'], 'ESCALATED', reference=True),
   'CLOSE': T(['RESOLVED', 'ESCALATED'], 'CLOSED', distinct=True)}),
 # D.28 Smart access events and device health
 dict(key='access_event', table='access_events', path='/access/events', parent='access_grant', parent_in_body=True, scope='PROPERTY', read='pms:read', write='pms:write',
  kinds=['ISSUE_COMMAND', 'REVOKE_COMMAND', 'MANUAL_KEY_HANDOVER'], initial='PENDING', event='access.grant.updated.v1',
  transitions={'RECORD_CONFIRMED': T(['PENDING', 'UNKNOWN'], 'CONFIRMED', reference=True, evidence=True), 'RECORD_FAILED': T(['PENDING', 'UNKNOWN'], 'FAILED', reference=True),
   'RECORD_UNKNOWN': T(['PENDING'], 'UNKNOWN'), 'RESOLVE_MANUALLY': T(['UNKNOWN', 'FAILED'], 'MANUALLY_RESOLVED', evidence=True, distinct=True)}),
 dict(key='device_health_snapshot', table='device_health_snapshots', path='/access/devices/{id}/health', parent='access_device', scope='PROPERTY', read='pms:read', write='pms:write',
  kinds=['HEARTBEAT', 'LOW_BATTERY', 'OFFLINE', 'TAMPER', 'FIRMWARE'], initial='RECORDED', event='access.device.alert.v1',
  transitions={'ACKNOWLEDGE': T(['RECORDED'], 'ACKNOWLEDGED'), 'RESOLVE': T(['RECORDED', 'ACKNOWLEDGED'], 'RESOLVED', evidence=True)}),
 # D.26 / D.27 Financing consent and insurance compliance
 dict(key='financing_consent', table='financing_consents', path='/financing/{id}/consents', parent='financing_offer', scope='PROPERTY', read='financing:read', write='financing:write',
  kinds=['DATA_SHARING', 'CREDIT_ENQUIRY', 'INCOME_VERIFICATION'], initial='GRANTED', event='financing.status.updated.v1', counterparty_required=True, ends_required=True,
  transitions={'REVOKE': T(['GRANTED'], 'REVOKED', evidence=True)}, system=['GRANTED>EXPIRED'], expire=[(['GRANTED'], 'EXPIRED', 'ends_at', 'CONSENT_EXPIRED')]),
 dict(key='insurance_compliance_check', table='insurance_compliance_checks', path='/insurance/compliance-checks', scope='PROPERTY', read='insurance:read', write='insurance:write',
  kinds=['PROPERTY', 'LIABILITY', 'CONTENTS', 'BUSINESS_INTERRUPTION', 'CONSTRUCTION'], initial='PENDING', event='insurance.compliance.updated.v1', ends_required=True,
  transitions={'RECORD_REMEDIATION': T(['NON_COMPLIANT'], 'REMEDIATED', evidence=True, reference=True), 'WAIVE': T(['NON_COMPLIANT'], 'WAIVED', mfa=True, distinct=True, evidence=True)},
  system=['PENDING>COMPLIANT', 'PENDING>NON_COMPLIANT']),
 # D.36 Partner platform credentials and webhooks
 dict(key='api_credential', table='api_credentials', path='/integrations/{id}/credentials', parent='integration_client', scope='COMPANY', read='integrations:read', write='integrations:write',
  kinds=['API_KEY_REF', 'OAUTH_CLIENT_REF', 'MTLS_CERT_REF'], initial='ACTIVE', event='integration.credential.updated.v1', ends_required=True, create_mfa=True,
  transitions={'START_ROTATION': T(['ACTIVE'], 'ROTATING', mfa=True, reference=True), 'COMPLETE_ROTATION': T(['ROTATING'], 'ACTIVE', mfa=True), 'REVOKE': T(['ACTIVE', 'ROTATING'], 'REVOKED')},
  system=['ACTIVE>EXPIRED', 'ROTATING>EXPIRED'], expire=[(['ACTIVE', 'ROTATING'], 'EXPIRED', 'ends_at', 'CREDENTIAL_EXPIRED')], revoke_route=True),
 dict(key='webhook_subscription', table='webhook_subscriptions', path='/integrations/{id}/webhooks', parent='integration_client', scope='COMPANY', read='integrations:read', write='integrations:write',
  kinds=['RESERVATION_EVENTS', 'INVENTORY_EVENTS', 'FOLIO_EVENTS', 'GUEST_EVENTS', 'FINANCE_EVENTS'], initial='PENDING_VERIFICATION', event='integration.webhook.updated.v1',
  transitions={'VERIFY': T(['PENDING_VERIFICATION'], 'ACTIVE', mfa=True, reference=True), 'PAUSE': T(['ACTIVE'], 'PAUSED'), 'RESUME': T(['PAUSED'], 'ACTIVE', mfa=True),
   'REVOKE': T(['PENDING_VERIFICATION', 'ACTIVE', 'PAUSED'], 'REVOKED')}),
 # D.37 FX references
 dict(key='fx_rate_ref', table='fx_rate_refs', path='/fx-rates', scope='COMPANY', read='tax:read', write='tax:write',
  kinds=['SPOT', 'SETTLEMENT', 'DISPLAY'], initial='RECORDED', event='fiscal.policy.updated.v1', starts_required=True, ends_required=True,
  transitions={'ACTIVATE': T(['RECORDED'], 'ACTIVE', mfa=True, distinct=True), 'RETRACT': T(['RECORDED', 'ACTIVE'], 'RETRACTED', mfa=True, evidence=True)},
  system=['ACTIVE>STALE', 'ACTIVE>SUPERSEDED'], expire=[(['ACTIVE'], 'STALE', 'ends_at', 'FX_RATE_STALE')]),
 # D.25 Organisation change and closure
 dict(key='organisation_change_case', table='organisation_change_cases', path='/business-changes', scope='COMPANY', read='organisation:read', write='organisation:write',
  kinds=['NAME_CHANGE', 'BENEFICIAL_OWNER_CHANGE', 'REPRESENTATIVE_CHANGE', 'MERGER', 'ACQUISITION', 'REGISTERED_ADDRESS_CHANGE'], initial='DRAFT', event='business.change.updated.v1',
  transitions={'SUBMIT': T(['DRAFT'], 'SUBMITTED', evidence=True), 'RECORD_VERIFICATION': T(['SUBMITTED'], 'VERIFIED', reference=True),
   'APPROVE': T(['VERIFIED'], 'APPROVED', cap='organisation:verify', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_ORGANISATION_CHANGE', 'organisation:verify', False)),
   'APPLY': T(['APPROVED'], 'APPLIED', mfa=True), 'REJECT': T(['SUBMITTED', 'VERIFIED'], 'REJECTED', distinct=True), 'WITHDRAW': T(['DRAFT', 'SUBMITTED'], 'WITHDRAWN')}),
 dict(key='closure_case', table='closure_cases', path='/closure-cases', scope='COMPANY', read='organisation:read', write='organisation:write',
  kinds=['VOLUNTARY_CLOSURE', 'DORMANCY', 'REGULATORY_CLOSURE', 'MERGED_INTO_SUCCESSOR'], initial='DRAFT', event='business.closure.updated.v1', unique_live_scope=['DRAFT', 'OBLIGATIONS_REVIEW', 'APPROVED', 'RETENTION'],
  transitions={'START_REVIEW': T(['DRAFT'], 'OBLIGATIONS_REVIEW'), 'APPROVE': T(['OBLIGATIONS_REVIEW'], 'APPROVED', cap='organisation:verify', mfa=True, approval=('COMMERCIAL', 'COMMERCIAL_BUSINESS_CLOSURE', 'organisation:verify', False)),
   'ENTER_RETENTION': T(['APPROVED'], 'RETENTION', mfa=True), 'CLOSE': T(['RETENTION'], 'CLOSED', mfa=True, distinct=True), 'CANCEL': T(['DRAFT', 'OBLIGATIONS_REVIEW', 'APPROVED'], 'CANCELLED')}),
 # D.17 / D.14 / D.15 hospitality supplements
 dict(key='housekeeping_checklist', table='housekeeping_checklists', path='/housekeeping/checklists', scope='PROPERTY', read='housekeeping:read', write='housekeeping:write',
  kinds=['TURNOVER', 'STAYOVER', 'DEEP_CLEAN', 'INSPECTION_ONLY', 'REWORK'], initial='DRAFT', event='housekeeping.task.updated.v1',
  transitions={'ACTIVATE': T(['DRAFT'], 'ACTIVE'), 'RETIRE': T(['DRAFT', 'ACTIVE'], 'RETIRED')}),
 dict(key='lost_found_item', table='lost_found_items', path='/lost-found', scope='PROPERTY', read='housekeeping:read', write='housekeeping:write',
  kinds=['ELECTRONICS', 'DOCUMENT', 'JEWELLERY', 'CLOTHING', 'CASH', 'OTHER'], initial='FOUND', event='housekeeping.lost_found.updated.v1',
  transitions={'STORE': T(['FOUND'], 'STORED', evidence=True), 'RECORD_CLAIM': T(['STORED'], 'CLAIMED', reference=True), 'RECORD_RETURN': T(['CLAIMED'], 'RETURNED', evidence=True, distinct=True),
   'DISPOSE': T(['STORED'], 'DISPOSED', mfa=True, distinct=True, evidence=True)}),
 dict(key='linen_stock', table='linen_stock_refs', path='/linen-stock', scope='PROPERTY', read='housekeeping:read', write='housekeeping:write',
  kinds=['BED_LINEN', 'TOWELS', 'BATHROBES', 'TABLE_LINEN', 'AMENITIES'], initial='ACTIVE', event='housekeeping.stock.updated.v1', unique_live=['ACTIVE'], subject='ASSET_OR_SPACE', subject_required=True,
  transitions={'RECORD_COUNT': T(['ACTIVE'], 'ACTIVE', evidence=True), 'RETIRE': T(['ACTIVE'], 'RETIRED')}),
 dict(key='shortlet_setting', table='shortlet_settings', path='/shortlet/settings', scope='PROPERTY', read='pms:read', write='pms:write',
  kinds=['HOUSE_RULES'], initial='DRAFT', event='shortlet.settings.updated.v1', unique_live_scope=['ACTIVE'],
  transitions={'ACTIVATE': T(['DRAFT'], 'ACTIVE', mfa=True), 'SUPERSEDE': T(['ACTIVE'], 'SUPERSEDED'), 'DISCARD': T(['DRAFT'], 'DISCARDED')}),
 dict(key='property_service', table='property_service_catalogue', path='/hotel/services', scope='PROPERTY', read='products:read', write='products:write',
  kinds=['SPA', 'DINING', 'LAUNDRY', 'TRANSFER', 'PARKING', 'EXPERIENCE', 'BUSINESS_CENTRE', 'OTHER'], initial='DRAFT', event='hotel.service.updated.v1', amount_required=True,
  transitions={'ACTIVATE': T(['DRAFT', 'SUSPENDED'], 'ACTIVE'), 'SUSPEND': T(['ACTIVE'], 'SUSPENDED'), 'RETIRE': T(['DRAFT', 'ACTIVE', 'SUSPENDED'], 'RETIRED')}),
 # D.41 Branch incidents
 dict(key='branch_incident', table='branch_incidents', path='/branch-incidents', scope='PROPERTY', read='pms:read', write='pms:write',
  kinds=['CONNECTIVITY_LOSS', 'POWER_OUTAGE', 'SYSTEM_OUTAGE', 'DATA_RECOVERY', 'SECURITY_EVENT'], initial='OPEN', event='reconciliation.required.v1', starts_required=True,
  transitions={'MITIGATE': T(['OPEN'], 'MITIGATING', assignee=True), 'RESOLVE': T(['OPEN', 'MITIGATING'], 'RESOLVED', evidence=True), 'CLOSE': T(['RESOLVED'], 'CLOSED', distinct=True)}),
 # D.40 Content intelligence
 dict(key='content_suggestion', table='content_suggestions', path='/content-suggestions', scope='PROPERTY', read='content:read', write='content:write',
  kinds=['DESCRIPTION', 'AMENITY', 'ROOM_TYPE_FACT', 'PHOTO_TAG', 'SPACE_GRAPH', 'ACCESSIBILITY'], initial='SUGGESTED', event='content.quality.changed.v1', subject='ASSET_OR_SPACE',
  transitions={'ACCEPT': T(['SUGGESTED'], 'ACCEPTED', distinct=True), 'ACCEPT_EDITED': T(['SUGGESTED'], 'ACCEPTED', distinct=True), 'REJECT': T(['SUGGESTED'], 'REJECTED'),
   'PUBLISH': T(['ACCEPTED'], 'PUBLISHED', reference=True)}, system=['SUGGESTED>EXPIRED'], expire=[(['SUGGESTED'], 'EXPIRED', 'ends_at', 'SUGGESTION_EXPIRED')]),
 # D.23 / D.30 Analytics supplements
 dict(key='experiment', table='experiments', path='/revenue/experiments', scope='PROPERTY', read='rms:read', write='rms:write',
  kinds=['CONTENT', 'PACKAGE', 'PROMOTION', 'MERCHANDISING'], initial='DRAFT', event='analytics.experiment.updated.v1', ends_required=True,
  transitions={'START': T(['DRAFT'], 'RUNNING', mfa=True, distinct=True), 'STOP': T(['RUNNING'], 'ABORTED', evidence=False), 'RECORD_RESULT': T(['RUNNING'], 'COMPLETED', evidence=True)},
  system=['RUNNING>COMPLETED']),
 dict(key='market_benchmark', table='market_benchmarks', path='/market-benchmarks', scope='PROPERTY', read='analytics:read', write='rms:write',
  kinds=['ADR', 'OCCUPANCY', 'REVPAR', 'RENT_PER_SQM', 'SALE_PRICE_PER_SQM', 'DEMAND_INDEX'], initial='RECORDED', event='analytics.benchmark.updated.v1', starts_required=True, ends_required=True,
  transitions={'ACTIVATE': T(['RECORDED'], 'ACTIVE', distinct=True), 'RETRACT': T(['RECORDED', 'ACTIVE'], 'RETRACTED', evidence=True)}, system=['ACTIVE>STALE'],
  expire=[(['ACTIVE'], 'STALE', 'ends_at', 'BENCHMARK_STALE')]),
]
for _d in DOMAINS_38:
    _d['migration'] = 38
ALL_DOMAINS = DOMAINS + DOMAINS_38

def states(d):
    out = [d['initial']] + d.get('extra_states', [])
    for t in d['transitions'].values():
        for s in t['frm'] + [t['to']]:
            if s not in out:
                out.append(s)
    for e in d.get('system', []):
        for s in e.split('>'):
            if s not in out:
                out.append(s)
    return out

def edges(d):
    out = []
    for t in d['transitions'].values():
        for f in t['frm']:
            e = f + '>' + t['to']
            if e not in out:
                out.append(e)
    for e in d.get('system', []):
        if e not in out:
            out.append(e)
    return out

HEADER_36 = ['-- +goose Up', '-- GENERATED by scripts/contracts/business_ops_domains.py. Section 16 governed', '-- operational domains with database-enforced state machines. Subjects,', '-- parents and scopes are tenant-bound; evidence is Media references only.', '']
HEADER_38 = ['-- +goose Up', '-- GENERATED by scripts/contracts/business_ops_domains.py. Annex D lifecycles', '-- (D.9-D.42) previously deferred in the database catalogue, as governed', '-- domains with database-enforced state machines. Additive only: no existing', '-- table, row or migration is modified. Money settlement truth stays with', '-- Payment (financial_obligations readback); evidence is Media references only.', '']

def sql(domains=None, header=None):
    domains = DOMAINS if domains is None else domains
    lines = list(HEADER_36 if header is None else header)
    for d in domains:
        st = ','.join("'" + s + "'" for s in states(d))
        kinds = ','.join("'" + k + "'" for k in d['kinds'])
        parent = d.get('parent')
        fk = []
        if d['scope'] in ('PROPERTY', 'NODE'):
            fk.append('FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id)')
        if parent and parent != 'DEVELOPMENT':
            ptable = next(x['table'] for x in ALL_DOMAINS if x['key'] == parent)
            fk.append(f'FOREIGN KEY(organisation_id,parent_id) REFERENCES {ptable}(organisation_id,id)')
        if parent == 'DEVELOPMENT' or d['scope'] in ('DEVELOPMENT', 'PARCEL', 'NODE'):
            fk.append('FOREIGN KEY(organisation_id,scope_id) REFERENCES inventory_nodes(organisation_id,id)')
        checks = [f"CHECK(kind IN ({kinds}))", f"CHECK(state IN ({st}))", "CHECK(scope_kind IN ('PROPERTY','PARCEL'))" if d['scope'] == 'NODE' else f"CHECK(scope_kind='{d['scope']}')"]
        if d['scope'] == 'NODE':
            checks.append("CHECK(scope_kind='PROPERTY' AND property_id IS NOT NULL AND property_id=scope_id OR scope_kind='PARCEL' AND property_id IS NULL)")
        if d['scope'] == 'PROPERTY':
            checks.append('CHECK(property_id IS NOT NULL AND property_id=scope_id)')
        if d['scope'] == 'COMPANY':
            checks.append('CHECK(scope_id=organisation_id)')
        if d.get('subject_required'):
            checks.append('CHECK(subject_id IS NOT NULL)')
        if d.get('counterparty_required'):
            checks.append("CHECK(length(counterparty_reference)>=3)")
        if d.get('amount_required'):
            checks.append('CHECK(amount_minor IS NOT NULL AND currency IS NOT NULL)')
        if d.get('starts_required'):
            checks.append('CHECK(starts_at IS NOT NULL)')
        if d.get('ends_required'):
            checks.append('CHECK(ends_at IS NOT NULL AND (starts_at IS NULL OR ends_at>starts_at))')
        if parent:
            checks.append('CHECK(parent_id IS NOT NULL)' if parent != 'DEVELOPMENT' else 'CHECK(parent_id IS NULL)')
        lines.append(f"CREATE TABLE {d['table']} (")
        lines.append(" id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),scope_kind text NOT NULL,scope_id uuid NOT NULL,property_id uuid,parent_id uuid,subject_id uuid,")
        lines.append(" kind text NOT NULL,state text NOT NULL,title text NOT NULL CHECK(length(title) BETWEEN 3 AND 200),counterparty_reference text NOT NULL DEFAULT '' CHECK(length(counterparty_reference)<=300),external_reference text NOT NULL DEFAULT '' CHECK(length(external_reference)<=300),")
        lines.append(" amount_minor bigint CHECK(amount_minor IS NULL OR amount_minor BETWEEN 0 AND 1000000000000000),currency text CHECK(currency IS NULL OR currency~'^[A-Z]{3}$'),starts_at timestamptz,ends_at timestamptz,")
        lines.append(" attributes jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(attributes)='object' AND octet_length(attributes::text)<=16384),evidence_refs jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(evidence_refs)='array' AND jsonb_array_length(evidence_refs)<=50),")
        lines.append(" approval_id uuid,assigned_to uuid REFERENCES business_accounts(id),last_actor_id uuid REFERENCES business_accounts(id),last_action text NOT NULL DEFAULT 'CREATED',closed_at timestamptz,")
        lines.append(" version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),")
        lines.append(" CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK((amount_minor IS NULL)=(currency IS NULL)),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),")
        lines.append(' ' + ','.join(checks + fk) + ',UNIQUE(organisation_id,id)')
        lines.append(');')
        lines.append(f"CREATE INDEX {d['table']}_scope ON {d['table']}(organisation_id,scope_id,state,created_at DESC,id);")
        if d.get('unique_live'):
            lines.append(f"CREATE UNIQUE INDEX {d['table']}_one_live ON {d['table']}(organisation_id,subject_id) WHERE state IN ({','.join(chr(39)+s+chr(39) for s in d['unique_live'])});")
        if d.get('unique_live_scope'):
            lines.append(f"CREATE UNIQUE INDEX {d['table']}_one_live_scope ON {d['table']}(organisation_id,scope_id) WHERE state IN ({','.join(chr(39)+s+chr(39) for s in d['unique_live_scope'])});")
        if d.get('subject', '').startswith('RECORD:'):
            lines.append(f"CREATE INDEX {d['table']}_subject ON {d['table']}(organisation_id,subject_id,state);")
        if d.get('migration') == 38 and d.get('parent') and d.get('parent') != 'DEVELOPMENT':
            lines.append(f"CREATE INDEX {d['table']}_parent ON {d['table']}(organisation_id,parent_id,state);")
        if d.get('unique_live_parent'):
            lines.append(f"CREATE UNIQUE INDEX {d['table']}_one_live_parent ON {d['table']}(organisation_id,parent_id) WHERE state IN ({','.join(chr(39)+s+chr(39) for s in d['unique_live_parent'])});")
        lines.append(f"CREATE TRIGGER {d['table']}_guard BEFORE UPDATE OR DELETE ON {d['table']} FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('{','.join(edges(d))}');")
        lines.append('')
    lines += ['-- +goose Down', 'SELECT 1;', '']
    return '\n'.join(lines)

def go_str(v):
    return '"' + v + '"'

def go():
    out = ['// Code generated by scripts/contracts/business_ops_domains.py. DO NOT EDIT.', '', 'package businesshttp', '', 'import (', '\t"net/http"', '', '\t"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"', '\t"github.com/go-chi/chi/v5"', ')', '', 'var _ = approvals.ActionExternalManagerAccess', '']
    out.append('var opsDomains = map[string]*opsDomain{')
    for d in ALL_DOMAINS:
        out.append(f"\t{go_str(d['key'])}: {{")
        out.append(f"\t\tKey: {go_str(d['key'])}, Table: {go_str(d['table'])}, Path: {go_str(d['path'])}, Scope: {go_str(d['scope'])}, Read: {go_str(d['read'])}, Write: {go_str(d['write'])}, Initial: {go_str(d['initial'])}, Event: {go_str(d['event'])},")
        out.append(f"\t\tKinds: []string{{{', '.join(go_str(k) for k in d['kinds'])}}}, States: []string{{{', '.join(go_str(s) for s in states(d))}}},")
        out.append(f"\t\tParent: {go_str(d.get('parent', ''))}, ParentInBody: {str(bool(d.get('parent_in_body'))).lower()}, Subject: {go_str(d.get('subject', ''))}, SubjectRequired: {str(bool(d.get('subject_required'))).lower()}, CounterpartyRequired: {str(bool(d.get('counterparty_required'))).lower()},")
        out.append(f"\t\tAmountRequired: {str(bool(d.get('amount_required'))).lower()}, StartsRequired: {str(bool(d.get('starts_required'))).lower()}, EndsRequired: {str(bool(d.get('ends_required'))).lower()}, CreateMFA: {str(bool(d.get('create_mfa'))).lower()},")
        ca = d.get('create_approval')
        edge_list = f"Edges: []string{{{', '.join(go_str(e) for e in edges(d))}}},"
        if ca:
            out.append(f"\t\tCreateApproval: &opsApproval{{Privilege: approvals.{ca[1]}}}, {edge_list}")
        else:
            out.append(f"\t\t{edge_list}")
        out.append('\t\tTransitions: map[string]opsTransition{')
        width = max(len(go_str(n)) + 1 for n in d['transitions'])
        for name, t in d['transitions'].items():
            appr = 'nil'
            if t['approval']:
                a = t['approval']
                appr = f"&opsApproval{{Commercial: {go_str(a[1])}, Capability: {go_str(a[2])}, BindAmount: {str(a[3]).lower()}}}" if a[0] == 'COMMERCIAL' else f"&opsApproval{{Privilege: approvals.{a[1]}}}"
            out.append(f"\t\t\t{(go_str(name) + ':').ljust(width)} {{From: []string{{{', '.join(go_str(f) for f in t['frm'])}}}, To: {go_str(t['to'])}, Capability: {go_str(t['cap'] or '')}, MFA: {str(t['mfa']).lower()}, Approval: {appr}, Evidence: {str(t['evidence']).lower()}, Distinct: {str(t['distinct']).lower()}, Assignee: {str(t['assignee']).lower()}, Amount: {str(t['amount']).lower()}, Reference: {str(t['reference']).lower()}}},")
        out.append('\t\t},')
        out.append('\t},')
    out.append('}')
    out.append('')
    out.append('// opsGeneratedSweeps are the declarative date-driven system transitions')
    out.append('// (expiry, due, overdue, staleness) applied by the operations worker.')
    out.append('var opsGeneratedSweeps = []opsSweep{')
    for d in ALL_DOMAINS:
        for frm, to, col, action in d.get('expire', []):
            states_sql = ','.join("'" + x + "'" for x in frm)
            out.append(f"\t{{{go_str(d['key'])}, `SELECT organisation_id,id FROM {d['table']} WHERE state IN ({states_sql}) AND {col}<=$1 ORDER BY {col},id LIMIT $2`, {go_str(to)}, {go_str(action)}}},")
        if d.get('expiring'):
            frm, to, days, action = d['expiring']
            out.append(f"\t{{{go_str(d['key'])}, `SELECT organisation_id,id FROM {d['table']} WHERE state='{frm}' AND ends_at<=$1::timestamptz+interval '{days} days' AND ends_at>$1 ORDER BY ends_at,id LIMIT $2`, {go_str(to)}, {go_str(action)}}},")
    out.append('}')
    out.append('')
    # handlers and registration (explicit so contract generation can map them)
    out.append('func (h *Handler) registerOpsDomains(r chi.Router) {')
    for d in ALL_DOMAINS:
        n = camel(d['key'])
        base = d['path']
        item = base + ('/{record_id}' if '{id}' in base else '/{id}')
        out.append(f'\tr.Get({go_str(base)}, h.ops{n}List)')
        out.append(f'\tr.Post({go_str(base)}, h.ops{n}Create)')
        out.append(f'\tr.Get({go_str(item)}, h.ops{n}Get)')
        out.append(f'\tr.Post({go_str(item + "/transitions")}, h.ops{n}Transition)')
        if d.get('patch'):
            out.append(f'\tr.Patch({go_str(item)}, h.ops{n}Transition)')
        if d.get('revoke_route'):
            out.append(f'\tr.Post({go_str(item + "/revoke")}, h.ops{n}Revoke)')
        if d.get('approve_route'):
            out.append(f'\tr.Post({go_str(item + "/approve")}, h.ops{n}Approve)')
    out.append('}')
    out.append('')
    for d in ALL_DOMAINS:
        n = camel(d['key'])
        k = go_str(d['key'])
        funcs = [('List', f'h.opsDomainList(w, r, opsDomains[{k}])'), ('Create', f'h.opsDomainCreate(w, r, opsDomains[{k}])'), ('Get', f'h.opsDomainGet(w, r, opsDomains[{k}])'), ('Transition', f'h.opsDomainTransition(w, r, opsDomains[{k}], "")')]
        if d.get('revoke_route'):
            funcs.append(('Revoke', f'h.opsDomainTransition(w, r, opsDomains[{k}], "REVOKE")'))
        if d.get('approve_route'):
            funcs.append(('Approve', f'h.opsDomainTransition(w, r, opsDomains[{k}], "APPROVE")'))
        for suffix, body in funcs:
            out.append(f'func (h *Handler) ops{n}{suffix}(w http.ResponseWriter, r *http.Request) {{')
            out.append('\t' + body)
            out.append('}')
    out.append('')
    return '\n'.join(out)

def camel(s):
    return ''.join(p.capitalize() for p in s.split('_'))

def contract_entries():
    """Handler -> (capability, request schema, response schema, status, mfa)."""
    data = {}
    for d in ALL_DOMAINS:
        n = camel(d['key'])
        data[f'ops{n}List'] = (d['read'], None, 'OpsRecordList', 200, False)
        data[f'ops{n}Create'] = (d['write'], 'OpsRecordCreate', 'OpsRecord', 201, bool(d.get('create_mfa')))
        data[f'ops{n}Get'] = (d['read'], None, 'OpsRecordDetail', 200, False)
        mfa = any(t['mfa'] for t in d['transitions'].values())
        data[f'ops{n}Transition'] = (d['write'], 'OpsRecordTransition', 'OpsRecord', 200, mfa)
        if d.get('revoke_route'):
            data[f'ops{n}Revoke'] = (d['write'], 'OpsRecordTransition', 'OpsRecord', 200, False)
        if d.get('approve_route'):
            data[f'ops{n}Approve'] = (d['write'], 'OpsRecordTransition', 'OpsRecord', 200, True)
    return data

def domain_notes():
    notes = {}
    for d in ALL_DOMAINS:
        n = camel(d['key'])
        desc = f"{d['key']} governed lifecycle (states {', '.join(states(d))}). Transitions: " + '; '.join(
            f"{a} {'/'.join(t['frm'])}->{t['to']}" + (' MFA' if t['mfa'] else '') + (' exact independent approval' if t['approval'] else '') + (' evidence' if t['evidence'] else '') + (' distinct actor' if t['distinct'] else '')
            for a, t in d['transitions'].items()) + '. Database-enforced edges; version and Idempotency-Key required.'
        for suffix in ('List', 'Create', 'Get', 'Transition', 'Revoke', 'Approve'):
            notes[f'ops{n}{suffix}'] = desc
    return notes

if __name__ == '__main__':
    outputs = {SERVICE / 'migrations/00036_property_operations.sql': sql(), SERVICE / 'migrations/00038_annex_d_lifecycles.sql': sql(DOMAINS_38, HEADER_38), SERVICE / 'internal/http/operational_domains_generated.go': go()}
    changed = []
    for p, text in outputs.items():
        if not p.exists() or p.read_text(encoding='utf-8') != text:
            changed.append(str(p.relative_to(ROOT)))
            if '--check' not in sys.argv:
                p.write_text(text, encoding='utf-8', newline='\n')
    if '--check' in sys.argv and changed:
        raise SystemExit('Domain drift: ' + ', '.join(changed))
    print(f'{len(ALL_DOMAINS)} governed domains ' + ('verified' if '--check' in sys.argv else 'generated'))
