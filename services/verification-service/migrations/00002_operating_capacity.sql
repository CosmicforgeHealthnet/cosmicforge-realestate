-- +goose Up
-- Operating-capacity eligibility (e.g. HOSPITALITY) as a first-class
-- verification type for business subjects. The capacity code is bound by the
-- case's caller_reference ("capacity:<CODE>").
ALTER TABLE verification_cases DROP CONSTRAINT verification_cases_verification_type_check;
ALTER TABLE verification_cases ADD CONSTRAINT verification_cases_verification_type_check
    CHECK (verification_type IN ('IDENTITY','BUSINESS','PROPERTY_AUTHORITY','PROFESSIONAL_CREDENTIAL','OPERATING_CAPACITY'));

-- +goose Down
SELECT 1;
