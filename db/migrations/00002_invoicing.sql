-- +goose Up
-- Invoicing — tock-owned tables (phase 7). No upstream counterpart: invoicing
-- lives in a private extension of solidtime, so these names are ours. They are
-- additive: the upstream schema in 00001 is untouched.

CREATE TABLE public.invoice_settings (
    organization_id uuid NOT NULL,
    seller_name character varying(255),
    seller_vatin character varying(255),
    seller_address_line_1 character varying(255),
    seller_address_line_2 character varying(255),
    seller_address_line_3 character varying(255),
    seller_address_post_code character varying(255),
    seller_address_city character varying(255),
    seller_address_country character varying(255),
    seller_phone character varying(255),
    seller_email character varying(255),
    footer_default text,
    notes_default text,
    tax_rate_default integer,
    e_invoicing_enabled boolean DEFAULT false NOT NULL,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone,
    CONSTRAINT invoice_settings_pkey PRIMARY KEY (organization_id),
    CONSTRAINT invoice_settings_organization_id_foreign FOREIGN KEY (organization_id)
        REFERENCES public.organizations (id) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE public.invoice_recipients (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    vatin character varying(255),
    address_line_1 character varying(255),
    address_line_2 character varying(255),
    address_line_3 character varying(255),
    address_post_code character varying(255),
    address_city character varying(255),
    address_country character varying(255),
    phone character varying(255),
    email character varying(255),
    archived_at timestamp(0) without time zone,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone,
    CONSTRAINT invoice_recipients_pkey PRIMARY KEY (id),
    CONSTRAINT invoice_recipients_organization_id_foreign FOREIGN KEY (organization_id)
        REFERENCES public.organizations (id) ON UPDATE CASCADE ON DELETE CASCADE
);
CREATE INDEX invoice_recipients_organization_id_index ON public.invoice_recipients (organization_id);

CREATE TABLE public.invoices (
    id uuid NOT NULL,
    organization_id uuid NOT NULL,
    invoice_recipient_id uuid NOT NULL,
    status character varying(255) DEFAULT 'draft' NOT NULL,
    date date NOT NULL,
    due_at date,
    paid_date date,
    reference character varying(255) NOT NULL,
    seller_name character varying(255) NOT NULL,
    seller_vatin character varying(255),
    seller_address_line_1 character varying(255),
    seller_address_line_2 character varying(255),
    seller_address_line_3 character varying(255),
    seller_address_post_code character varying(255),
    seller_address_city character varying(255),
    seller_address_country character varying(255),
    seller_phone character varying(255),
    seller_email character varying(255),
    currency character varying(3) NOT NULL,
    billing_period_start date,
    billing_period_end date,
    tax_rate integer DEFAULT 0 NOT NULL,
    discount_amount bigint DEFAULT 0 NOT NULL,
    discount_type character varying(255) DEFAULT 'fixed' NOT NULL,
    payment_iban character varying(255),
    footer text,
    notes text,
    payment_terms text,
    is_eu_reverse_charge boolean DEFAULT false NOT NULL,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone,
    CONSTRAINT invoices_pkey PRIMARY KEY (id),
    CONSTRAINT invoices_organization_id_foreign FOREIGN KEY (organization_id)
        REFERENCES public.organizations (id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT invoices_invoice_recipient_id_foreign FOREIGN KEY (invoice_recipient_id)
        REFERENCES public.invoice_recipients (id) ON UPDATE CASCADE ON DELETE RESTRICT,
    CONSTRAINT invoices_status_check CHECK (status::text = ANY (ARRAY['draft', 'sent', 'paid', 'cancelled'])),
    CONSTRAINT invoices_discount_type_check CHECK (discount_type::text = ANY (ARRAY['percentage', 'fixed']))
);
CREATE UNIQUE INDEX invoices_organization_id_reference_unique ON public.invoices (organization_id, reference);
CREATE INDEX invoices_organization_id_status_index ON public.invoices (organization_id, status);

CREATE TABLE public.invoice_items (
    id uuid NOT NULL,
    invoice_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    description text,
    unit_price bigint NOT NULL,
    quantity numeric(12,4) NOT NULL,
    order_index integer NOT NULL,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone,
    CONSTRAINT invoice_items_pkey PRIMARY KEY (id),
    CONSTRAINT invoice_items_invoice_id_foreign FOREIGN KEY (invoice_id)
        REFERENCES public.invoices (id) ON UPDATE CASCADE ON DELETE CASCADE
);
CREATE INDEX invoice_items_invoice_id_order_index_index ON public.invoice_items (invoice_id, order_index);

-- +goose Down
DROP TABLE public.invoice_items;
DROP TABLE public.invoices;
DROP TABLE public.invoice_recipients;
DROP TABLE public.invoice_settings;
