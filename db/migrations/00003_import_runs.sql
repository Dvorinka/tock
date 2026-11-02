-- +goose Up
CREATE TABLE public.import_runs (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES public.organizations (id) ON UPDATE CASCADE ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES public.users (id) ON UPDATE CASCADE ON DELETE CASCADE,
    importer_type character varying(255) NOT NULL,
    timezone character varying(255) NOT NULL,
    data bytea,
    status character varying(32) NOT NULL DEFAULT 'queued',
    report jsonb,
    error text,
    created_at timestamp(0) without time zone NOT NULL DEFAULT now(),
    updated_at timestamp(0) without time zone NOT NULL DEFAULT now(),
    completed_at timestamp(0) without time zone,
    CONSTRAINT import_runs_status_check CHECK (status IN ('queued', 'processing', 'completed', 'failed'))
);
CREATE INDEX import_runs_organization_created_index ON public.import_runs (organization_id, created_at DESC);

-- +goose Down
DROP TABLE public.import_runs;
