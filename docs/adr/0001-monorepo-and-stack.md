# ADR-0001: Monorepo and Core Technology Stack

**Status:** Accepted

## Decision

Use a single monorepo. Backend services are written in Go. Mobile applications use Flutter/Dart. Web applications use Next.js/React/TypeScript. PostgreSQL is the primary transactional database and Redis is used for justified ephemeral workloads. CF Meet owns chat, voice and video communications.

## Rationale

This keeps platform conventions consistent with other CosmicForge systems, reduces duplicated infrastructure, makes cross-service contracts auditable, and avoids rebuilding communications capabilities already provided by CF Meet.
