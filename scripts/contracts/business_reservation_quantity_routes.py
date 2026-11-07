"""Section 9 quantity-level fragmentation and automatic waitlist contracts."""


def register_reservation_quantity(s, obj, arr, ref, typ):
    digest = {'type': 'string', 'pattern': '^[a-f0-9]{64}$', 'example': 'a' * 64}
    s['ReservationQuantityChange']['properties']['action']['enum'] = ['PARTIAL_CANCEL', 'SPLIT', 'MERGE']
    s['ReservationQuantityChange']['properties']['request_digest'].update(digest)
    for field in ('transferred_room_id', 'cancelled_room_id', 'approval_id'):
        s['ReservationQuantityChange']['properties'][field] = {'anyOf': [typ('ids.ID'), {'type': 'null'}]}
    for prefix in ('original', 'remaining', 'transferred', 'released'):
        s['ReservationQuantityChange']['properties'][prefix + '_accommodation_minor'] = {'type': ['integer', 'null']}
    s['ReservationQuantityChangeDay']['properties']['day'].update(format='date', example='2027-01-02')
    s['ReservationQuantityCommand']['properties']['quantity'].update(minimum=1, maximum=999)
    s['WaitlistAutomationPolicyCommand']['properties']['mode']['enum'] = ['AUTOMATIC_OFFERS', 'MANUAL']
    s['WaitlistAutomationPolicyCommand']['properties']['offer_seconds'].update(minimum=30, maximum=300)
    s['WaitlistAutomationPolicyCommand']['properties']['max_offers_per_entry'].update(minimum=1, maximum=10)
    s['WaitlistAutomationPolicyCommand']['properties']['max_offers_per_run'].update(minimum=1, maximum=50)
    s['WaitlistAutomationPolicy']['properties']['mode']['enum'] = ['AUTOMATIC_OFFERS', 'MANUAL']
    s['WaitlistAutomationPolicy']['properties']['approval_id'] = {'anyOf': [typ('ids.ID'), {'type': 'null'}]}
    s['WaitlistAutomationRun']['properties']['trigger']['enum'] = ['RELEASE_SIGNAL', 'NEW_ENTRY', 'MANUAL', 'SWEEP']
    s['WaitlistAutomationRun']['properties']['state']['enum'] = ['OFFERED', 'NO_ELIGIBLE_CANDIDATE', 'POLICY_INACTIVE', 'AUTHORITY_REVOKED', 'PROPERTY_INELIGIBLE']
    s['WaitlistAutomationRun']['properties']['evaluations'] = arr({'type': 'object', 'additionalProperties': True})
    for field in ('policy_id',):
        s['WaitlistAutomationRun']['properties'][field] = {'anyOf': [typ('ids.ID'), {'type': 'null'}]}
    s['WaitlistAutomationRun']['properties']['policy_version'] = {'type': ['integer', 'null']}
    s['WaitlistAutomationRunList']['properties']['lease_owner'] = {'type': ['string', 'null']}
    s['WaitlistAutomationRunList']['properties']['lease_expires_at'] = {'type': ['string', 'null'], 'format': 'date-time'}
    s['WaitlistAutomationTriggered']['properties']['executed'] = {'const': False}
    return {
        'reservationQuantityCancel': ('reservations:cancel', 'ReservationQuantityCommand', 'ReservationQuantityResult', 200, False),
        'reservationQuantitySplit': ('reservations:modify', 'ReservationQuantityCommand', 'ReservationQuantityResult', 200, True),
        'reservationQuantityMerge': ('reservations:modify', 'ReservationQuantityMergeCommand', 'ReservationQuantityResult', 200, True),
        'reservationQuantityChanges': ('reservations:read', None, 'ReservationQuantityChangeList', 200, False),
        'reservationWaitlistAutomationPolicies': ('reservations:read', None, 'WaitlistAutomationPolicyList', 200, False),
        'reservationWaitlistAutomationPolicySave': ('reservations:write', 'WaitlistAutomationPolicyCommand', 'WaitlistAutomationPolicy', 201, True),
        'reservationWaitlistAutomationRuns': ('reservations:read', None, 'WaitlistAutomationRunList', 200, False),
        'reservationWaitlistAutomationTrigger': ('reservations:write', 'WaitlistAutomationTrigger', 'WaitlistAutomationTriggered', 202, False),
    }


def reservation_quantity_notes(handler):
    if handler in ('reservationQuantityCancel', 'reservationQuantitySplit', 'reservationQuantityMerge'):
        return ('Quantity-level changes apply to a confirmed or in-house line whose every accepted night is committed at exactly the line quantity, with no physical assignment, stay, group block, rooming entry, outstanding overbooking or unresolved collection. '
                'The highest-numbered slots are detached or cancelled so every remaining slot keeps its frozen amount; accepted lines are superseded, never edited. '
                'An immutable ledger records per date original = remaining + transferred + released committed quantity and the conserved amount/accommodation split. '
                'Partial cancellation releases stock and records a frozen-policy PARTIAL_CANCELLED obligation; refunds stay pending Payment readback. '
                'Split and merge need recent MFA and an exact independent COMMERCIAL_RESERVATION_LINEAGE approval bound to the moved amount; merges are limited to one split family and one source contract/provider. ')
    if handler.startswith('reservationWaitlistAutomation'):
        return ('AUTOMATIC_OFFERS needs recent MFA and an exact independent COMMERCIAL_RESERVATION_POLICY_CHANGE approval bound to {waitlist_automation: command}; MANUAL versions pause immediately. '
                'Database release signals from every stock producer drive a fenced, lease-based worker that offers released stock by priority, fewest prior automatic offers, then arrival, with an atomic canonical hold per offer and at most one live offer per entry. '
                'Offers expire and release their holds; the next candidate is selected automatically. Only guest acceptance through the existing accept command creates a reservation. A run request returns 202 and never executes inventory changes itself. ')
    return ''


def reservation_quantity_queries(handler):
    if handler in ('reservationWaitlistAutomationPolicies', 'reservationWaitlistAutomationRuns'):
        return [('property_id', 'uuid', True)]
    return None


def reservation_quantity_examples(uid):
    reason = 'Versioned quantity change with current operator evidence'
    return {
        'ReservationQuantityCommand': {'version': 2, 'room_id': uid, 'room_version': 1, 'quantity': 1, 'guest_ids': [], 'new_reservation': False, 'reason': reason},
        'ReservationQuantityMergeCommand': {'version': 3, 'room_id': uid, 'room_version': 1, 'source_reservation_id': uid, 'source_version': 2, 'source_room_id': uid, 'source_room_version': 1, 'approval_id': uid, 'reason': reason},
        'WaitlistAutomationPolicyCommand': {'property_id': uid, 'mode': 'AUTOMATIC_OFFERS', 'channel': '', 'offer_seconds': 300, 'max_offers_per_entry': 3, 'max_offers_per_run': 5, 'effective_at': '2026-11-01T00:00:00Z', 'expires_at': '2027-11-01T00:00:00Z', 'approval_id': uid, 'reason': 'Independently approved automatic waitlist offers'},
        'WaitlistAutomationTrigger': {'property_id': uid, 'reason': 'Request an automatic waitlist evaluation now'},
    }
