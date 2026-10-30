# Business Operations bounded-context boundaries

These are ownership boundaries for future sections. A directory does not mean its workflows are implemented. Contexts use only the Business Operations database and shared platform primitives.

| Context | Business Operations responsibility | External authority/reference boundary |
|---|---|---|
| auth | Business-domain accounts, credentials, sessions and devices | Shared cryptographic/session primitives; no central identity service |
| organisation | Organisation records and lifecycle | Verification Service decides KYB status; store case/result references |
| workforce | Business staff membership and work roles | Business Operations is membership/RBAC authority |
| approvals | Business-side approval policies and decisions | Calls/commands may reference external operations; never executes payments |
| finance | Business obligations, allocation context and financial references | Payment Service owns money movement, ledger and reconciliation |
| portfolio | Business portfolio composition and ownership links | Verification Service remains authority for verified property authority |
| authority | Business evidence references and authority workflow context | Verification Service owns cases, evidence decisions and statuses |
| content | Listing and operational content projections | Media & Document Service owns bytes, upload/download authorization and lifecycle |
| inventory | Business-managed property, units, rooms, availability and inventory | Marketplace consumes a projection; Business Operations is canonical supply |
| distribution | Publication intent and channel publication state | Marketplace owns discovery/distribution journeys; optional channels use adapters |
| revenue | Business revenue plans, rates and operational reporting inputs | Payment Service remains financial transaction authority |
| crm | Business customer/account context and support linkage | Notification Service delivers messages; CF Meet owns human conversation history |
| reservations | Canonical business-side reservation operations | Marketplace owns customer booking journey; Payment Service owns payment execution |
| residential | Residential operating records | References canonical portfolio/inventory; no separate property authority |
| commercial | Commercial operating records | References canonical portfolio/inventory; no separate property authority |
| land | Land/property development operating records | Verification Service decides authority; references canonical portfolio |
| sales | Business-side buyer and sales operations | Payment Service owns collection/transfer financial records; external legal authority stays external |
| development | Development, building and construction operations | Artisan and Professional Services own provider profiles and execution records |
| shortlet | Short-let business operations and stays | Marketplace owns customer discovery/booking; Payment Service handles money movement |
| hospitality | Hotel/hospitality operating records | Notification Service delivers notices; external channel providers remain optional adapters |
| housekeeping | Business work allocation and housekeeping state | External providers retain execution profiles; no provider marketplace authority |
| maintenance | Business maintenance requests, approvals and history references | Artisan Service owns artisan profiles and service execution records |
| delegation | Business-side mandate, delegated scope and duration | Property Manager Service owns manager profiles/workspaces |
| professional | Business-side appointment and transaction associations | Professional Services Service owns provider profiles and execution workflows |
| reputation | Business-side responses and operational reputation context | Customer-facing marketplace projection belongs to Marketplace Service |
| support | Business-side support context and escalation links | Notification owns delivery; CF Meet owns messages, calls and conversation history |
| analytics | Business operational read models and metrics | Must consume authorized APIs/events; no cross-service database reads |
| automation | Business-side rules and scheduled operational actions | External side effects go through owning services and approved adapters |
| risk | Business operational risk flags and review context | Verification/payment decisions remain authoritative in their owning services |
| privacy | Business-domain privacy requests and policy state | Media/document lifecycle remains in Media & Document Service |
| financing | Business financing applications/context and external references | Lender/provider execution is external; Payment Service owns actual payment/ledger effects |
| insurance | Business insurance policy/context references | Insurer execution is external; Verification Service retains professional/licence decisions |
| access | Business-side access authorization and assignment context | Smart-access provider integrations are optional; no device-provider authority |
| integrations | Typed authenticated adapters and client contracts | Never import another service's implementation or connect to its database |
| events | Business event schemas, outbox/event consumers and idempotency integration | Shared envelope and JetStream are reused; consumers tolerate at-least-once delivery |
| workers | Process registration, lifecycle and worker composition | Worker jobs use local repositories or authorized service contracts only |

Common restrictions: no cross-service SQL, no payment-provider SDK/calls, no raw media/document storage, no duplicate verification authority, no notification provider delivery and no locally authoritative human messaging history.
