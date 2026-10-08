-- +goose Up
CREATE TABLE b (id uuid PRIMARY KEY, a_id uuid NOT NULL REFERENCES a (id));
-- +goose Down
DROP TABLE b;
