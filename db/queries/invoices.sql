-- Invoicing queries (phase 7). tock-owned tables.

-- name: ListInvoiceRecipients :many
SELECT r.id, r.organization_id, r.name, r.vatin, r.address_line_1, r.address_line_2,
       r.address_line_3, r.address_post_code, r.address_city, r.address_country,
       r.phone, r.email, r.archived_at, r.created_at, r.updated_at,
       (SELECT count(*) FROM invoices i WHERE i.invoice_recipient_id = r.id) AS invoices_count,
       EXISTS (SELECT 1 FROM invoices i WHERE i.invoice_recipient_id = r.id AND i.status <> 'draft') AS has_non_draft_invoices
FROM invoice_recipients r
WHERE r.organization_id = sqlc.arg('organization_id')
ORDER BY r.name, r.id;

-- name: GetInvoiceRecipient :one
SELECT r.id, r.organization_id, r.name, r.vatin, r.address_line_1, r.address_line_2,
       r.address_line_3, r.address_post_code, r.address_city, r.address_country,
       r.phone, r.email, r.archived_at, r.created_at, r.updated_at,
       (SELECT count(*) FROM invoices i WHERE i.invoice_recipient_id = r.id) AS invoices_count,
       EXISTS (SELECT 1 FROM invoices i WHERE i.invoice_recipient_id = r.id AND i.status <> 'draft') AS has_non_draft_invoices
FROM invoice_recipients r
WHERE r.id = sqlc.arg('id');

-- name: InsertInvoiceRecipient :exec
INSERT INTO invoice_recipients (id, organization_id, name, vatin, address_line_1,
    address_line_2, address_line_3, address_post_code, address_city, address_country,
    phone, email, archived_at, created_at, updated_at)
VALUES (sqlc.arg('id'), sqlc.arg('organization_id'), sqlc.arg('name'), sqlc.arg('vatin'),
    sqlc.arg('address_line_1'), sqlc.arg('address_line_2'), sqlc.arg('address_line_3'),
    sqlc.arg('address_post_code'), sqlc.arg('address_city'), sqlc.arg('address_country'),
    sqlc.arg('phone'), sqlc.arg('email'), sqlc.arg('archived_at'), now(), now());

-- name: UpdateInvoiceRecipient :exec
UPDATE invoice_recipients
SET name = sqlc.arg('name'),
    vatin = sqlc.arg('vatin'),
    address_line_1 = sqlc.arg('address_line_1'),
    address_line_2 = sqlc.arg('address_line_2'),
    address_line_3 = sqlc.arg('address_line_3'),
    address_post_code = sqlc.arg('address_post_code'),
    address_city = sqlc.arg('address_city'),
    address_country = sqlc.arg('address_country'),
    phone = sqlc.arg('phone'),
    email = sqlc.arg('email'),
    archived_at = sqlc.arg('archived_at'),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: DeleteInvoiceRecipient :exec
DELETE FROM invoice_recipients WHERE id = sqlc.arg('id');

-- name: GetInvoiceSettings :one
SELECT organization_id, seller_name, seller_vatin, seller_address_line_1,
       seller_address_line_2, seller_address_line_3, seller_address_post_code,
       seller_address_city, seller_address_country, seller_phone, seller_email,
       footer_default, notes_default, tax_rate_default, e_invoicing_enabled,
       created_at, updated_at
FROM invoice_settings
WHERE organization_id = sqlc.arg('organization_id');

-- name: UpsertInvoiceSettings :one
INSERT INTO invoice_settings (organization_id, seller_name, seller_vatin,
    seller_address_line_1, seller_address_line_2, seller_address_line_3,
    seller_address_post_code, seller_address_city, seller_address_country,
    seller_phone, seller_email, footer_default, notes_default, tax_rate_default,
    e_invoicing_enabled, created_at, updated_at)
VALUES (sqlc.arg('organization_id'), sqlc.arg('seller_name'), sqlc.arg('seller_vatin'),
    sqlc.arg('seller_address_line_1'), sqlc.arg('seller_address_line_2'),
    sqlc.arg('seller_address_line_3'), sqlc.arg('seller_address_post_code'),
    sqlc.arg('seller_address_city'), sqlc.arg('seller_address_country'),
    sqlc.arg('seller_phone'), sqlc.arg('seller_email'), sqlc.arg('footer_default'),
    sqlc.arg('notes_default'), sqlc.arg('tax_rate_default'),
    sqlc.arg('e_invoicing_enabled'), now(), now())
ON CONFLICT (organization_id) DO UPDATE SET
    seller_name = excluded.seller_name,
    seller_vatin = excluded.seller_vatin,
    seller_address_line_1 = excluded.seller_address_line_1,
    seller_address_line_2 = excluded.seller_address_line_2,
    seller_address_line_3 = excluded.seller_address_line_3,
    seller_address_post_code = excluded.seller_address_post_code,
    seller_address_city = excluded.seller_address_city,
    seller_address_country = excluded.seller_address_country,
    seller_phone = excluded.seller_phone,
    seller_email = excluded.seller_email,
    footer_default = excluded.footer_default,
    notes_default = excluded.notes_default,
    tax_rate_default = excluded.tax_rate_default,
    e_invoicing_enabled = excluded.e_invoicing_enabled,
    updated_at = now()
RETURNING organization_id, seller_name, seller_vatin, seller_address_line_1,
          seller_address_line_2, seller_address_line_3, seller_address_post_code,
          seller_address_city, seller_address_country, seller_phone, seller_email,
          footer_default, notes_default, tax_rate_default, e_invoicing_enabled,
          created_at, updated_at;

-- name: ListInvoicesPaginated :many
SELECT i.id, i.organization_id, i.invoice_recipient_id, i.status, i.date, i.due_at,
       i.paid_date, i.reference, i.seller_name, i.seller_vatin,
       i.seller_address_line_1, i.seller_address_line_2, i.seller_address_line_3,
       i.seller_address_post_code, i.seller_address_city, i.seller_address_country,
       i.seller_phone, i.seller_email, i.currency, i.billing_period_start,
       i.billing_period_end, i.tax_rate, i.discount_amount, i.discount_type,
       i.payment_iban, i.footer, i.notes, i.payment_terms, i.is_eu_reverse_charge,
       i.created_at, i.updated_at, r.name AS recipient_name
FROM invoices i
JOIN invoice_recipients r ON r.id = i.invoice_recipient_id
WHERE i.organization_id = sqlc.arg('organization_id')
  AND (i.status = coalesce(sqlc.narg('status')::text, i.status))
ORDER BY i.date DESC, i.created_at DESC, i.id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountInvoices :one
SELECT count(*)
FROM invoices i
WHERE i.organization_id = sqlc.arg('organization_id')
  AND (i.status = coalesce(sqlc.narg('status')::text, i.status));

-- name: GetInvoice :one
SELECT i.id, i.organization_id, i.invoice_recipient_id, i.status, i.date, i.due_at,
       i.paid_date, i.reference, i.seller_name, i.seller_vatin,
       i.seller_address_line_1, i.seller_address_line_2, i.seller_address_line_3,
       i.seller_address_post_code, i.seller_address_city, i.seller_address_country,
       i.seller_phone, i.seller_email, i.currency, i.billing_period_start,
       i.billing_period_end, i.tax_rate, i.discount_amount, i.discount_type,
       i.payment_iban, i.footer, i.notes, i.payment_terms, i.is_eu_reverse_charge,
       i.created_at, i.updated_at, r.name AS recipient_name
FROM invoices i
JOIN invoice_recipients r ON r.id = i.invoice_recipient_id
WHERE i.id = sqlc.arg('id');

-- name: InsertInvoice :exec
INSERT INTO invoices (id, organization_id, invoice_recipient_id, status, date, due_at,
    paid_date, reference, seller_name, seller_vatin, seller_address_line_1,
    seller_address_line_2, seller_address_line_3, seller_address_post_code,
    seller_address_city, seller_address_country, seller_phone, seller_email,
    currency, billing_period_start, billing_period_end, tax_rate, discount_amount,
    discount_type, payment_iban, footer, notes, payment_terms, is_eu_reverse_charge,
    created_at, updated_at)
VALUES (sqlc.arg('id'), sqlc.arg('organization_id'), sqlc.arg('invoice_recipient_id'),
    sqlc.arg('status'), sqlc.arg('date'), sqlc.arg('due_at'), sqlc.arg('paid_date'),
    sqlc.arg('reference'), sqlc.arg('seller_name'), sqlc.arg('seller_vatin'),
    sqlc.arg('seller_address_line_1'), sqlc.arg('seller_address_line_2'),
    sqlc.arg('seller_address_line_3'), sqlc.arg('seller_address_post_code'),
    sqlc.arg('seller_address_city'), sqlc.arg('seller_address_country'),
    sqlc.arg('seller_phone'), sqlc.arg('seller_email'), sqlc.arg('currency'),
    sqlc.arg('billing_period_start'), sqlc.arg('billing_period_end'),
    sqlc.arg('tax_rate'), sqlc.arg('discount_amount'), sqlc.arg('discount_type'),
    sqlc.arg('payment_iban'), sqlc.arg('footer'), sqlc.arg('notes'),
    sqlc.arg('payment_terms'), sqlc.arg('is_eu_reverse_charge'), now(), now());

-- name: UpdateInvoice :exec
UPDATE invoices
SET invoice_recipient_id = sqlc.arg('invoice_recipient_id'),
    status = sqlc.arg('status'),
    date = sqlc.arg('date'),
    due_at = sqlc.arg('due_at'),
    paid_date = sqlc.arg('paid_date'),
    reference = sqlc.arg('reference'),
    seller_name = sqlc.arg('seller_name'),
    seller_vatin = sqlc.arg('seller_vatin'),
    seller_address_line_1 = sqlc.arg('seller_address_line_1'),
    seller_address_line_2 = sqlc.arg('seller_address_line_2'),
    seller_address_line_3 = sqlc.arg('seller_address_line_3'),
    seller_address_post_code = sqlc.arg('seller_address_post_code'),
    seller_address_city = sqlc.arg('seller_address_city'),
    seller_address_country = sqlc.arg('seller_address_country'),
    seller_phone = sqlc.arg('seller_phone'),
    seller_email = sqlc.arg('seller_email'),
    currency = sqlc.arg('currency'),
    billing_period_start = sqlc.arg('billing_period_start'),
    billing_period_end = sqlc.arg('billing_period_end'),
    tax_rate = sqlc.arg('tax_rate'),
    discount_amount = sqlc.arg('discount_amount'),
    discount_type = sqlc.arg('discount_type'),
    payment_iban = sqlc.arg('payment_iban'),
    footer = sqlc.arg('footer'),
    notes = sqlc.arg('notes'),
    payment_terms = sqlc.arg('payment_terms'),
    is_eu_reverse_charge = sqlc.arg('is_eu_reverse_charge'),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: DeleteInvoice :exec
DELETE FROM invoices WHERE id = sqlc.arg('id');

-- name: InvoiceReferenceExists :one
SELECT EXISTS (
    SELECT 1 FROM invoices
    WHERE organization_id = sqlc.arg('organization_id')
      AND reference = sqlc.arg('reference')
      AND (sqlc.narg('exclude_id')::uuid IS NULL OR id <> sqlc.narg('exclude_id')::uuid)
) AS exists;

-- name: ListInvoiceItems :many
SELECT id, invoice_id, name, description, unit_price, quantity, order_index,
       created_at, updated_at
FROM invoice_items
WHERE invoice_id = sqlc.arg('invoice_id')
ORDER BY order_index, id;

-- name: DeleteInvoiceItems :exec
DELETE FROM invoice_items WHERE invoice_id = sqlc.arg('invoice_id');

-- name: InsertInvoiceItem :exec
INSERT INTO invoice_items (id, invoice_id, name, description, unit_price, quantity,
    order_index, created_at, updated_at)
VALUES (sqlc.arg('id'), sqlc.arg('invoice_id'), sqlc.arg('name'), sqlc.arg('description'),
    sqlc.arg('unit_price'), sqlc.arg('quantity'), sqlc.arg('order_index'), now(), now());
