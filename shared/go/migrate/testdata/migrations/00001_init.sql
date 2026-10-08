-- +goose Up
CREATE TABLE a (id uuid PRIMARY KEY);
-- +goose Down
DROP TABLE a;
