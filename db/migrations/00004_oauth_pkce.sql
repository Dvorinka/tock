-- +goose Up
-- PKCE material must survive a process restart. The upstream Passport table
-- has no challenge column, so these nullable columns are additive.
ALTER TABLE public.oauth_auth_codes
    ADD COLUMN IF NOT EXISTS code_challenge text,
    ADD COLUMN IF NOT EXISTS code_challenge_method character varying(16);

-- +goose Down
ALTER TABLE public.oauth_auth_codes
    DROP COLUMN IF EXISTS code_challenge,
    DROP COLUMN IF EXISTS code_challenge_method;
