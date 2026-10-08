# ADR-0005: UUIDv7 Identifiers

**Status:** Accepted

Persistent externally referenced entities use UUIDv7.

Reasoning:
- globally unique without a central ID service;
- time-ordered enough for improved index locality compared with fully random UUIDs;
- safe for service-local generation;
- avoids exposing sequential row counts.

Cross-domain references include both the owner/domain and UUID where ownership could be ambiguous.
