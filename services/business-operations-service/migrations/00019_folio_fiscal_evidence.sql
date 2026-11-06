-- +goose Up
CREATE UNIQUE INDEX folio_item_fiscal_parent ON folio_items(organisation_id,folio_id,id);
CREATE TABLE folio_fiscal_reviews (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,folio_id uuid NOT NULL,folio_item_id uuid NOT NULL,reservation_room_id uuid NOT NULL,source_quote_id uuid NOT NULL,
 review_version bigint NOT NULL CHECK(review_version>0),expected_folio_version bigint NOT NULL CHECK(expected_folio_version>0),state text NOT NULL CHECK(state IN ('ITEMIZED','LEGACY_AGGREGATE_REVIEW_REQUIRED','OPERATOR_REVIEWED')),
 source_kind text NOT NULL CHECK(source_kind IN ('ACCEPTED_QUOTE','OPERATOR_ATTESTED')),breakdown jsonb NOT NULL CHECK(jsonb_typeof(breakdown)='object' AND octet_length(breakdown::text)<=1048576),digest text NOT NULL CHECK(digest~'^[a-f0-9]{64}$'),
 approval_id uuid,evidence_reference text NOT NULL DEFAULT '',reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 2000),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,folio_id,folio_item_id) REFERENCES folio_items(organisation_id,folio_id,id),FOREIGN KEY(organisation_id,reservation_room_id) REFERENCES reservation_rooms(organisation_id,id),FOREIGN KEY(organisation_id,source_quote_id) REFERENCES price_quotes(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),CHECK((source_kind='OPERATOR_ATTESTED')=(approval_id IS NOT NULL)),CHECK(source_kind<>'OPERATOR_ATTESTED' OR state='OPERATOR_REVIEWED' AND created_by IS NOT NULL AND length(evidence_reference) BETWEEN 5 AND 300),CHECK(source_kind<>'ACCEPTED_QUOTE' OR state IN ('ITEMIZED','LEGACY_AGGREGATE_REVIEW_REQUIRED')),
 UNIQUE(organisation_id,id),UNIQUE(folio_item_id,review_version)
);
CREATE INDEX folio_fiscal_current ON folio_fiscal_reviews(organisation_id,property_id,folio_id,folio_item_id,review_version DESC);
CREATE TRIGGER folio_fiscal_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_fiscal_reviews FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_folio_fiscal_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source folio_items%ROWTYPE;folio_version bigint;prior_version bigint;component_sum numeric;accommodation_total numeric;addon_total numeric;BEGIN
 PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 SELECT * INTO source FROM folio_items WHERE organisation_id=NEW.organisation_id AND folio_id=NEW.folio_id AND id=NEW.folio_item_id;
 IF NOT FOUND OR source.kind<>'ACCOMMODATION' OR source.policy_snapshot->'accepted_quote' IS NULL THEN RAISE EXCEPTION 'Fiscal detail requires its original accepted gross posting' USING ERRCODE='23514';END IF;
 SELECT version INTO folio_version FROM folios WHERE organisation_id=NEW.organisation_id AND id=NEW.folio_id;
 SELECT coalesce(max(review_version),0) INTO prior_version FROM folio_fiscal_reviews WHERE organisation_id=NEW.organisation_id AND folio_item_id=NEW.folio_item_id;
 IF NEW.review_version<>prior_version+1 OR NEW.expected_folio_version<>folio_version THEN RAISE EXCEPTION 'Fiscal review version changed' USING ERRCODE='23514';END IF;
 SELECT sum((p->>'amount_minor')::numeric) INTO component_sum FROM jsonb_array_elements(NEW.breakdown->'components') p;
 IF NOT coalesce(NEW.breakdown->>'financial_effect'='DETAIL_ONLY' AND NEW.breakdown->>'state'=NEW.state AND (NEW.breakdown->>'quote_id')::uuid=NEW.source_quote_id AND NEW.source_quote_id=(source.policy_snapshot->'accepted_quote'->>'quote_id')::uuid AND NEW.breakdown->>'currency'=source.currency AND (NEW.breakdown->>'gross_minor')::bigint=source.amount_minor AND component_sum=source.amount_minor::numeric AND (NEW.breakdown->>'slot')::integer=(source.policy_snapshot->>'slot')::integer AND (NEW.breakdown->>'original_quantity')::integer=(source.policy_snapshot->>'quantity')::integer AND NEW.reservation_room_id=(source.policy_snapshot->>'reservation_room_id')::uuid AND jsonb_array_length(NEW.breakdown->'components')>0,false) OR EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.breakdown->'components') p WHERE NOT coalesce(p->>'amount_minor'~'^[0-9]+$' AND p->>'kind' IN ('ACCOMMODATION_NET','ADD_ON_NET','TAX','FEE','ACCEPTED_SERVICES_NET'),false)) THEN
 RAISE EXCEPTION 'Fiscal detail must conserve its immutable gross source and identity' USING ERRCODE='23514';END IF;
 IF EXISTS(SELECT 1 FROM folio_fiscal_reviews r WHERE r.organisation_id=NEW.organisation_id AND r.reservation_room_id=NEW.reservation_room_id AND r.folio_item_id<>NEW.folio_item_id AND r.breakdown->>'slot'=NEW.breakdown->>'slot') THEN RAISE EXCEPTION 'An accepted quantity slot cannot be charged by two original gross postings' USING ERRCODE='23514';END IF;

 SELECT coalesce(sum((p->>'amount_minor')::numeric) FILTER(WHERE p->>'kind'='ACCOMMODATION_NET'),0),coalesce(sum((p->>'amount_minor')::numeric) FILTER(WHERE p->>'kind'='ADD_ON_NET'),0) INTO accommodation_total,addon_total FROM (
 SELECT NEW.breakdown AS detail UNION ALL
 SELECT r.breakdown FROM folio_fiscal_reviews r WHERE r.organisation_id=NEW.organisation_id AND r.reservation_room_id=NEW.reservation_room_id AND r.folio_item_id<>NEW.folio_item_id AND NOT EXISTS(SELECT 1 FROM folio_fiscal_reviews later WHERE later.organisation_id=r.organisation_id AND later.folio_item_id=r.folio_item_id AND later.review_version>r.review_version)
 ) sources CROSS JOIN LATERAL jsonb_array_elements(sources.detail->'components') p;
 IF accommodation_total>(source.policy_snapshot->'accepted_quote'->>'base_amount_minor')::numeric-(source.policy_snapshot->'accepted_quote'->>'discount_minor')::numeric OR addon_total>(source.policy_snapshot->'accepted_quote'->>'add_ons_minor')::numeric THEN RAISE EXCEPTION 'Fiscal attribution exceeds its original accepted source budget' USING ERRCODE='23514';END IF;

 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER folio_fiscal_source BEFORE INSERT ON folio_fiscal_reviews FOR EACH ROW EXECUTE FUNCTION business_folio_fiscal_source_guard();
-- +goose StatementBegin
CREATE FUNCTION business_folio_fiscal_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM hospitality_history h WHERE h.organisation_id=NEW.organisation_id AND h.entity_type='folio_fiscal_review' AND h.entity_id=NEW.id AND h.entity_version=1 AND h.change_context->>'digest'=NEW.digest) THEN RAISE EXCEPTION 'Fiscal review requires immutable transactional audit evidence' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER folio_fiscal_review_evidence AFTER INSERT ON folio_fiscal_reviews DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_folio_fiscal_evidence_guard();
-- +goose Down
SELECT 1;
