# Individual Owner — product and implementation documents

| File | Purpose |
|---|---|
| [`SECTION_24_FINAL_ACCEPTANCE_AND_APPENDIX_RECONCILIATION.md`](SECTION_24_FINAL_ACCEPTANCE_AND_APPENDIX_RECONCILIATION.md) | **Current final acceptance addendum**: all 15 Section 24 checklist items, Appendix C/D historical-to-current reconciliation, verified 35/35 CI and explicit staging/provider/legal release blockers. Source spec v1.0 remains immutable. |
| [`CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf`](CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf) | **Authoritative** *Individual Property Owner Platform — Full Backend Implementation Specification v1.0* (33 pp.; product source: V5 Corporate Product Feature Document, 22 July 2026; foundation baseline `54425cb`). SHA-256 `eccb0ece5f0b79d125f88549ae036849796f359f779ec3407c1704af710aa8af`. |
| [`CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.txt`](CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.txt) | Searchable text extraction of the PDF (the PDF wins on any discrepancy). |
| [`IMPLEMENTATION_COVERAGE.md`](IMPLEMENTATION_COVERAGE.md) | Requirement-level implementation register for spec Sections 5–12 and workstreams IO-1…IO-16. |
| [`LEGAL_POLICY.md`](LEGAL_POLICY.md) | Inventory of legal timelines and automatic decisions; versioned jurisdiction policy; what needs legal approval. |
| [`EVENT_COMPATIBILITY.md`](EVENT_COMPATIBILITY.md) | Versioned producer/consumer matrix for the seven consumed event families and their safety rules. |
| [`DATABASE_PREREQUISITES.md`](DATABASE_PREREQUISITES.md) | `btree_gist` deployment prerequisite and the migration preflight. |

The specification is versioned: a new revision is added as a new file (`…_v1.1.pdf`), never by
overwriting v1.0, so commits can be traced to the exact text they implemented.
