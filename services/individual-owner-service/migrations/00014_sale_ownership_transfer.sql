-- Ownership transfer after a completed sale (§13.4 / §13.19).
--
-- Completing a sale never changes the authoritative owner. The seller opens a
-- SALE ownership-change case linked to the completed sale; it runs through
-- the same guarded workflow as any transfer (step-up, identity gate,
-- co-owner consent, recipient acceptance, evidence and platform review).
-- A sale has at most one live or completed transfer case; a rejected or
-- cancelled case can be reopened.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE ownership_change_cases ADD COLUMN sale_id uuid REFERENCES sale_transactions (id);
ALTER TABLE ownership_change_cases DROP CONSTRAINT ownership_change_cases_case_type_check;
ALTER TABLE ownership_change_cases ADD CONSTRAINT ownership_change_cases_case_type_check
    CHECK (case_type IN ('TRANSFER','SUCCESSION','INCAPACITY','CORRECTION','SALE'));
ALTER TABLE ownership_change_cases ADD CONSTRAINT ownership_change_cases_sale_chk CHECK ((case_type = 'SALE') = (sale_id IS NOT NULL));
CREATE UNIQUE INDEX ownership_change_cases_sale_uniq ON ownership_change_cases (sale_id)
    WHERE sale_id IS NOT NULL AND status NOT IN ('REJECTED','CANCELLED');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS ownership_change_cases_sale_uniq;
ALTER TABLE ownership_change_cases DROP CONSTRAINT IF EXISTS ownership_change_cases_sale_chk;
ALTER TABLE ownership_change_cases DROP CONSTRAINT ownership_change_cases_case_type_check;
ALTER TABLE ownership_change_cases ADD CONSTRAINT ownership_change_cases_case_type_check
    CHECK (case_type IN ('TRANSFER','SUCCESSION','INCAPACITY','CORRECTION'));
ALTER TABLE ownership_change_cases DROP COLUMN IF EXISTS sale_id;
-- +goose StatementEnd
