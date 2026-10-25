package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/audit"
	"github.com/Dvorinka/tock/internal/domain"
)

// invoiceEntryInput is one validated line item.
type invoiceEntryInput struct {
	ID          pgtype.UUID
	Name        string
	Description *string
	UnitPrice   int64
	Quantity    float64
	OrderIndex  int32
}

// jsonDateField parses an optional date field. present reports whether the key
// was sent, isNull whether it was JSON null, ok whether it parsed.
func jsonDateField(body map[string]json.RawMessage, key string) (pgtype.Date, bool, bool, bool) {
	raw, present := body[key]
	if !present {
		return pgtype.Date{}, false, false, false
	}
	if string(raw) == "null" {
		return pgtype.Date{}, true, true, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return pgtype.Date{}, true, false, false
	}
	if t, err := time.Parse(ymdLayout, value); err == nil {
		return pgtype.Date{Time: t, Valid: true}, true, false, true
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return pgtype.Date{Time: t, Valid: true}, true, false, true
	}
	return pgtype.Date{}, true, false, false
}

// parseInvoiceBody validates the request body on top of `base` (the current
// row on update, zero on create) and returns the merged record plus the line
// items. `entriesGiven` reports whether the body replaced the item set.
func (s *Server) parseInvoiceBody(r *http.Request, body map[string]json.RawMessage, base invoiceRecord, create bool, scope *orgScope, errs map[string][]string) (invoiceRecord, []invoiceEntryInput, bool) {
	out := base
	out.OrganizationID = scope.org.ID
	if create {
		out.Status = invoiceStatusDraft
	}

	if !create {
		if _, present := body["status"]; present {
			if value, _, isString := jsonStringField(body, "status"); !isString || invoiceStatusLabels[value] == "" {
				errs["status"] = []string{"The selected status is invalid."}
			} else {
				out.Status = value
			}
		}
	}

	// date
	if value, present, isNull, ok := jsonDateField(body, "date"); present {
		switch {
		case isNull || !ok:
			if create {
				errs["date"] = []string{"The date field is required."}
			} else {
				errs["date"] = []string{"The date field must be a valid date."}
			}
		default:
			out.Date = value
		}
	} else if create {
		errs["date"] = []string{"The date field is required."}
	}

	// due_at / paid_date
	if value, present, isNull, ok := jsonDateField(body, "due_at"); present {
		switch {
		case isNull:
			out.DueAt = pgtype.Date{}
		case !ok:
			errs["due_at"] = []string{"The due at field must be a valid date."}
		default:
			out.DueAt = value
		}
	}
	if value, present, isNull, ok := jsonDateField(body, "paid_date"); present {
		switch {
		case isNull:
			out.PaidDate = pgtype.Date{}
		case !ok:
			errs["paid_date"] = []string{"The paid at field must be a valid date."}
		default:
			out.PaidDate = value
		}
	}
	if _, failed := errs["date"]; !failed {
		if out.DueAt.Valid && out.Date.Valid && out.DueAt.Time.Before(out.Date.Time) {
			errs["due_at"] = []string{"The due at field must be a date after or equal to date."}
		}
	}
	if create && out.Date.Valid && !out.DueAt.Valid && errs["due_at"] == nil {
		out.DueAt = out.Date
	}
	if out.Status == invoiceStatusPaid && !out.PaidDate.Valid {
		out.PaidDate = pgtype.Date{Time: time.Now().UTC(), Valid: true}
	}

	// reference
	if value, present, isString := jsonStringField(body, "reference"); present {
		switch {
		case !isString:
			errs["reference"] = []string{"The reference field must be a string."}
		case value == "":
			errs["reference"] = []string{"The reference field is required."}
		case len(value) > 255:
			errs["reference"] = []string{"The reference field must not be greater than 255 characters."}
		default:
			out.Reference = value
		}
	} else if create {
		errs["reference"] = []string{"The reference field is required."}
	} else if out.Reference == "" {
		errs["reference"] = []string{"The reference field is required."}
	}

	// seller_name
	if value, present, isString := jsonStringField(body, "seller_name"); present {
		switch {
		case !isString:
			errs["seller_name"] = []string{"The seller name field must be a string."}
		case value == "":
			errs["seller_name"] = []string{"The seller name field is required."}
		case len(value) > 255:
			errs["seller_name"] = []string{"The seller name field must not be greater than 255 characters."}
		default:
			out.SellerName = value
		}
	} else if create || out.SellerName == "" {
		errs["seller_name"] = []string{"The seller name field is required."}
	}

	applyNullableString(body, errs, "seller_vatin", 255, &out.SellerVatin)
	applyNullableString(body, errs, "seller_address_line_1", 255, &out.SellerAddressLine1)
	applyNullableString(body, errs, "seller_address_line_2", 255, &out.SellerAddressLine2)
	applyNullableString(body, errs, "seller_address_line_3", 255, &out.SellerAddressLine3)
	applyNullableString(body, errs, "seller_address_post_code", 255, &out.SellerAddressPostCode)
	applyNullableString(body, errs, "seller_address_city", 255, &out.SellerAddressCity)
	applyNullableString(body, errs, "seller_address_country", 255, &out.SellerAddressCountry)
	applyNullableString(body, errs, "seller_phone", 255, &out.SellerPhone)
	applyNullableString(body, errs, "seller_email", 255, &out.SellerEmail)
	applyNullableString(body, errs, "payment_iban", 255, &out.PaymentIban)
	applyNullableString(body, errs, "footer", 0, &out.Footer)
	applyNullableString(body, errs, "notes", 0, &out.Notes)
	applyNullableString(body, errs, "payment_terms", 0, &out.PaymentTerms)

	// currency
	if value, present, isString := jsonStringField(body, "currency"); present {
		switch {
		case !isString:
			errs["currency"] = []string{"The currency field must be a string."}
		case len(value) != 3:
			errs["currency"] = []string{"The currency field must be 3 characters."}
		default:
			out.Currency = strings.ToUpper(value)
		}
	} else if create || out.Currency == "" {
		errs["currency"] = []string{"The currency field is required."}
	}

	// billing period
	if value, present, isNull, ok := jsonDateField(body, "billing_period_start"); present {
		switch {
		case isNull:
			out.BillingPeriodStart = pgtype.Date{}
		case !ok:
			errs["billing_period_start"] = []string{"The billing period start field must be a valid date."}
		default:
			out.BillingPeriodStart = value
		}
	}
	if value, present, isNull, ok := jsonDateField(body, "billing_period_end"); present {
		switch {
		case isNull:
			out.BillingPeriodEnd = pgtype.Date{}
		case !ok:
			errs["billing_period_end"] = []string{"The billing period end field must be a valid date."}
		default:
			out.BillingPeriodEnd = value
		}
	}
	if out.BillingPeriodStart.Valid && out.BillingPeriodEnd.Valid &&
		out.BillingPeriodEnd.Time.Before(out.BillingPeriodStart.Time) &&
		errs["billing_period_end"] == nil {
		errs["billing_period_end"] = []string{"The billing period end field must be a date after or equal to billing period start."}
	}

	// tax_rate
	if value, present, valid := jsonInt(body, "tax_rate"); present {
		switch {
		case !valid:
			errs["tax_rate"] = []string{"The tax rate field must be an integer."}
		case value < 0:
			errs["tax_rate"] = []string{"The tax rate field must be at least 0."}
		case value > math.MaxInt32:
			errs["tax_rate"] = []string{"The tax rate field must not be greater than 2147483647."}
		default:
			out.TaxRate = int32(value)
		}
	}

	// discount
	if value, present, valid := jsonInt(body, "discount_amount"); present {
		switch {
		case !valid:
			errs["discount_amount"] = []string{"The discount amount field must be an integer."}
		case value < 0:
			errs["discount_amount"] = []string{"The discount amount field must be at least 0."}
		default:
			out.DiscountAmount = value
		}
	}
	if value, present, isString := jsonStringField(body, "discount_type"); present {
		switch {
		case !isString:
			errs["discount_type"] = []string{"The discount type field must be a string."}
		case value != discountTypePercentage && value != discountTypeFixed:
			errs["discount_type"] = []string{"The selected discount type is invalid."}
		default:
			out.DiscountType = value
		}
	} else if out.DiscountType == "" {
		out.DiscountType = discountTypeFixed
	}

	if _, present := body["is_eu_reverse_charge"]; present {
		if value, _, valid := jsonBool(body, "is_eu_reverse_charge"); !valid {
			errs["is_eu_reverse_charge"] = []string{"The is eu reverse charge field must be true or false."}
		} else {
			out.IsEuReverseCharge = value
		}
	}

	// invoice recipient
	if value, present, isString := jsonStringField(body, "invoice_recipient_id"); present {
		switch {
		case !isString || value == "":
			errs["invoice_recipient_id"] = []string{"The invoice recipient id field is required."}
		default:
			id, ok := domain.UUID(value)
			if !ok {
				errs["invoice_recipient_id"] = []string{"The selected invoice recipient id is invalid."}
				break
			}
			recipient, err := db.New(s.pool).GetInvoiceRecipient(r.Context(), id)
			if err != nil || recipient.OrganizationID != scope.org.ID {
				errs["invoice_recipient_id"] = []string{"The selected invoice recipient id is invalid."}
				break
			}
			out.InvoiceRecipientID = id
		}
	} else if create {
		errs["invoice_recipient_id"] = []string{"The invoice recipient id field is required."}
	}

	// entries
	entriesGiven := false
	var entries []invoiceEntryInput
	if raw, present := body["entries"]; present {
		entriesGiven = true
		entries = parseInvoiceEntries(raw, errs)
	}

	// reference uniqueness (Laravel's unique rule on the organization).
	if out.Reference != "" && errs["reference"] == nil {
		taken, err := db.New(s.pool).InvoiceReferenceExists(r.Context(), db.InvoiceReferenceExistsParams{
			OrganizationID: scope.org.ID, Reference: out.Reference, ExcludeID: base.ID,
		})
		if err == nil && taken {
			errs["reference"] = []string{"The reference has already been taken."}
		}
	}

	return out, entries, entriesGiven
}

// parseInvoiceEntries validates the items array, keying errors by their
// wildcard path (`entries.0.name`), which Laravel renders verbatim.
func parseInvoiceEntries(raw json.RawMessage, errs map[string][]string) []invoiceEntryInput {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		errs["entries"] = []string{"The entries field must be an array."}
		return nil
	}
	out := make([]invoiceEntryInput, 0, len(items))
	for i, item := range items {
		prefix := "entries." + strconv.Itoa(i)
		entry := invoiceEntryInput{OrderIndex: int32(i)}

		if value, _, isString := jsonStringField(item, "name"); !isString {
			errs[prefix+".name"] = []string{"The " + prefix + ".name field is required."}
		} else if value == "" {
			errs[prefix+".name"] = []string{"The " + prefix + ".name field is required."}
		} else if len(value) > 255 {
			errs[prefix+".name"] = []string{"The " + prefix + ".name field must not be greater than 255 characters."}
		} else {
			entry.Name = value
		}

		if _, present := item["description"]; present {
			if string(item["description"]) == "null" {
				entry.Description = nil
			} else if value, _, isString := jsonStringField(item, "description"); isString {
				entry.Description = &value
			} else {
				errs[prefix+".description"] = []string{"The " + prefix + ".description field must be a string."}
			}
		}

		if value, present, valid := jsonInt(item, "unit_price"); !present {
			errs[prefix+".unit_price"] = []string{"The " + prefix + ".unit_price field is required."}
		} else if !valid {
			errs[prefix+".unit_price"] = []string{"The " + prefix + ".unit_price field must be an integer."}
		} else if value < 0 {
			errs[prefix+".unit_price"] = []string{"The " + prefix + ".unit_price field must be at least 0."}
		} else {
			entry.UnitPrice = value
		}

		if quantity, present, valid := jsonFloat(item, "quantity"); !present {
			errs[prefix+".quantity"] = []string{"The " + prefix + ".quantity field is required."}
		} else if !valid {
			errs[prefix+".quantity"] = []string{"The " + prefix + ".quantity field must be a number."}
		} else if quantity < 0 {
			errs[prefix+".quantity"] = []string{"The " + prefix + ".quantity field must be at least 0."}
		} else if quantity > 99999999 {
			errs[prefix+".quantity"] = []string{"The " + prefix + ".quantity field must not be greater than 99999999."}
		} else {
			entry.Quantity = quantity
		}

		if id, present, isString := jsonStringField(item, "id"); present && isString {
			if parsed, ok := domain.UUID(id); ok {
				entry.ID = parsed
			}
		} else if present {
			errs[prefix+".id"] = []string{"The " + prefix + ".id field must be a string."}
		}
		out = append(out, entry)
	}
	return out
}

// --- handlers ---

// scopedInvoice loads {invoice} and verifies it belongs to the org.
func (s *Server) scopedInvoice(w http.ResponseWriter, r *http.Request, scope *orgScope) (invoiceRecord, bool) {
	raw := chi.URLParam(r, "invoice")
	id, ok := domain.UUID(raw)
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Invoice", raw))
		return invoiceRecord{}, false
	}
	row, err := db.New(s.pool).GetInvoice(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Invoice", raw))
		return invoiceRecord{}, false
	}
	if row.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return invoiceRecord{}, false
	}
	return invoiceRecordFromGet(row), true
}

// handleListInvoices: GET /api/v1/organizations/{organization}/invoices
func (s *Server) handleListInvoices(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:view")
	if !ok {
		return
	}
	var status *string
	if raw := r.URL.Query().Get("status"); raw != "" {
		if _, valid := invoiceStatusLabels[raw]; !valid {
			validationErr(w, map[string][]string{"status": {"The selected status is invalid."}})
			return
		}
		status = &raw
	}
	page := pageParam(r)
	q := db.New(s.pool)
	total, err := q.CountInvoices(r.Context(), db.CountInvoicesParams{
		OrganizationID: scope.org.ID, Status: status,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "count invoices", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rows, err := q.ListInvoicesPaginated(r.Context(), db.ListInvoicesPaginatedParams{
		OrganizationID: scope.org.ID, Status: status,
		PageLimit: defaultPerPage, PageOffset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "list invoices", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, invoiceListResource(invoiceRecordFromList(row)))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

// handleShowInvoice: GET .../invoices/{invoice}
func (s *Server) handleShowInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:view")
	if !ok {
		return
	}
	invoice, ok := s.scopedInvoice(w, r, scope)
	if !ok {
		return
	}
	s.writeInvoiceDetail(w, r, invoice)
}

func (s *Server) writeInvoiceDetail(w http.ResponseWriter, r *http.Request, invoice invoiceRecord) {
	q := db.New(s.pool)
	recipient, err := q.GetInvoiceRecipient(r.Context(), invoice.InvoiceRecipientID)
	if err != nil {
		slog.ErrorContext(r.Context(), "load invoice recipient", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	items, err := q.ListInvoiceItems(r.Context(), invoice.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list invoice items", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": invoiceDetailedResource(invoice, recipientRecordFromGet(recipient), items),
	})
}

// handleStoreInvoice: POST .../invoices
func (s *Server) handleStoreInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:create")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}
	invoice, entries, _ := s.parseInvoiceBody(r, body, invoiceRecord{ID: pgUUID()}, true, scope, errs)
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	invoice.ID = pgUUID()
	if err := s.insertInvoice(r.Context(), invoice, entries); err != nil {
		if duplicateInvoiceReference(err) {
			validationErr(w, map[string][]string{"reference": {"The reference has already been taken."}})
			return
		}
		slog.ErrorContext(r.Context(), "insert invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	saved, err := db.New(s.pool).GetInvoice(r.Context(), invoice.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventCreated, audit.TypeInvoice, invoiceRecordFromGet(saved).ID, scope.org.ID, pgtype.UUID{}, nil, invoiceAttrs(invoiceRecordFromGet(saved)))
	s.writeInvoiceDetail(w, r, invoiceRecordFromGet(saved))
}

// insertInvoice writes the invoice row and replaces its items.
func (s *Server) insertInvoice(ctx context.Context, invoice invoiceRecord, entries []invoiceEntryInput) error {
	q := db.New(s.pool)
	if err := q.InsertInvoice(ctx, db.InsertInvoiceParams{
		ID:                    invoice.ID,
		OrganizationID:        invoice.OrganizationID,
		InvoiceRecipientID:    invoice.InvoiceRecipientID,
		Status:                invoice.Status,
		Date:                  invoice.Date,
		DueAt:                 invoice.DueAt,
		PaidDate:              invoice.PaidDate,
		Reference:             invoice.Reference,
		SellerName:            invoice.SellerName,
		SellerVatin:           invoice.SellerVatin,
		SellerAddressLine1:    invoice.SellerAddressLine1,
		SellerAddressLine2:    invoice.SellerAddressLine2,
		SellerAddressLine3:    invoice.SellerAddressLine3,
		SellerAddressPostCode: invoice.SellerAddressPostCode,
		SellerAddressCity:     invoice.SellerAddressCity,
		SellerAddressCountry:  invoice.SellerAddressCountry,
		SellerPhone:           invoice.SellerPhone,
		SellerEmail:           invoice.SellerEmail,
		Currency:              invoice.Currency,
		BillingPeriodStart:    invoice.BillingPeriodStart,
		BillingPeriodEnd:      invoice.BillingPeriodEnd,
		TaxRate:               invoice.TaxRate,
		DiscountAmount:        invoice.DiscountAmount,
		DiscountType:          invoice.DiscountType,
		PaymentIban:           invoice.PaymentIban,
		Footer:                invoice.Footer,
		Notes:                 invoice.Notes,
		PaymentTerms:          invoice.PaymentTerms,
		IsEuReverseCharge:     invoice.IsEuReverseCharge,
	}); err != nil {
		return err
	}
	return s.replaceInvoiceItems(ctx, invoice.ID, entries)
}

func (s *Server) replaceInvoiceItems(ctx context.Context, invoiceID pgtype.UUID, entries []invoiceEntryInput) error {
	q := db.New(s.pool)
	if err := q.DeleteInvoiceItems(ctx, invoiceID); err != nil {
		return err
	}
	for _, entry := range entries {
		id := entry.ID
		if !id.Valid {
			id = pgUUID()
		}
		if err := q.InsertInvoiceItem(ctx, db.InsertInvoiceItemParams{
			ID:          id,
			InvoiceID:   invoiceID,
			Name:        entry.Name,
			Description: entry.Description,
			UnitPrice:   entry.UnitPrice,
			Quantity:    floatToNumeric(entry.Quantity),
			OrderIndex:  entry.OrderIndex,
		}); err != nil {
			return err
		}
	}
	return nil
}

// handleUpdateInvoice: PUT .../invoices/{invoice}
func (s *Server) handleUpdateInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:update")
	if !ok {
		return
	}
	current, ok := s.scopedInvoice(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}
	invoice, entries, entriesGiven := s.parseInvoiceBody(r, body, current, false, scope, errs)
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	invoice.ID = current.ID
	auditBefore := invoiceAttrs(current)
	if err := db.New(s.pool).UpdateInvoice(r.Context(), db.UpdateInvoiceParams{
		ID:                    invoice.ID,
		InvoiceRecipientID:    invoice.InvoiceRecipientID,
		Status:                invoice.Status,
		Date:                  invoice.Date,
		DueAt:                 invoice.DueAt,
		PaidDate:              invoice.PaidDate,
		Reference:             invoice.Reference,
		SellerName:            invoice.SellerName,
		SellerVatin:           invoice.SellerVatin,
		SellerAddressLine1:    invoice.SellerAddressLine1,
		SellerAddressLine2:    invoice.SellerAddressLine2,
		SellerAddressLine3:    invoice.SellerAddressLine3,
		SellerAddressPostCode: invoice.SellerAddressPostCode,
		SellerAddressCity:     invoice.SellerAddressCity,
		SellerAddressCountry:  invoice.SellerAddressCountry,
		SellerPhone:           invoice.SellerPhone,
		SellerEmail:           invoice.SellerEmail,
		Currency:              invoice.Currency,
		BillingPeriodStart:    invoice.BillingPeriodStart,
		BillingPeriodEnd:      invoice.BillingPeriodEnd,
		TaxRate:               invoice.TaxRate,
		DiscountAmount:        invoice.DiscountAmount,
		DiscountType:          invoice.DiscountType,
		PaymentIban:           invoice.PaymentIban,
		Footer:                invoice.Footer,
		Notes:                 invoice.Notes,
		PaymentTerms:          invoice.PaymentTerms,
		IsEuReverseCharge:     invoice.IsEuReverseCharge,
	}); err != nil {
		if duplicateInvoiceReference(err) {
			validationErr(w, map[string][]string{"reference": {"The reference has already been taken."}})
			return
		}
		slog.ErrorContext(r.Context(), "update invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if entriesGiven {
		if err := s.replaceInvoiceItems(r.Context(), current.ID, entries); err != nil {
			slog.ErrorContext(r.Context(), "replace invoice items", "error", err)
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	saved, err := db.New(s.pool).GetInvoice(r.Context(), current.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeInvoice, invoiceRecordFromGet(saved).ID, scope.org.ID, pgtype.UUID{}, auditBefore, invoiceAttrs(invoiceRecordFromGet(saved)))
	s.writeInvoiceDetail(w, r, invoiceRecordFromGet(saved))
}

// handleDestroyInvoice: DELETE .../invoices/{invoice}
func (s *Server) handleDestroyInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:delete")
	if !ok {
		return
	}
	invoice, ok := s.scopedInvoice(w, r, scope)
	if !ok {
		return
	}
	if err := db.New(s.pool).DeleteInvoice(r.Context(), invoice.ID); err != nil {
		slog.ErrorContext(r.Context(), "delete invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeInvoice, invoice.ID, scope.org.ID, pgtype.UUID{}, invoiceAttrs(invoice), nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleCopyInvoice: POST .../invoices/{invoice}/copy
//
// The copy keeps every field of the source, takes the submitted reference and
// starts again as a draft (an invoice number may only be issued once).
func (s *Server) handleCopyInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:create")
	if !ok {
		return
	}
	source, ok := s.scopedInvoice(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}
	reference, present, isString := jsonStringField(body, "reference")
	switch {
	case !present || !isString:
		errs["reference"] = []string{"The reference field is required."}
	case reference == "":
		errs["reference"] = []string{"The reference field is required."}
	case len(reference) > 255:
		errs["reference"] = []string{"The reference field must not be greater than 255 characters."}
	}
	if errs["reference"] == nil {
		taken, err := db.New(s.pool).InvoiceReferenceExists(r.Context(), db.InvoiceReferenceExistsParams{
			OrganizationID: scope.org.ID, Reference: reference,
		})
		if err == nil && taken {
			errs["reference"] = []string{"The reference has already been taken."}
		}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}

	items, err := db.New(s.pool).ListInvoiceItems(r.Context(), source.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list invoice items for copy", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	entries := make([]invoiceEntryInput, 0, len(items))
	for _, item := range items {
		entries = append(entries, invoiceEntryInput{
			Name:        item.Name,
			Description: item.Description,
			UnitPrice:   item.UnitPrice,
			Quantity:    numericToFloat(item.Quantity),
			OrderIndex:  item.OrderIndex,
		})
	}
	copied := source
	copied.ID = pgUUID()
	copied.Reference = reference
	copied.Status = invoiceStatusDraft
	copied.PaidDate = pgtype.Date{}
	if err := s.insertInvoice(r.Context(), copied, entries); err != nil {
		if duplicateInvoiceReference(err) {
			validationErr(w, map[string][]string{"reference": {"The reference has already been taken."}})
			return
		}
		slog.ErrorContext(r.Context(), "copy invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	saved, err := db.New(s.pool).GetInvoice(r.Context(), copied.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload copied invoice", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventCreated, audit.TypeInvoice, invoiceRecordFromGet(saved).ID, scope.org.ID, pgtype.UUID{}, nil, invoiceAttrs(invoiceRecordFromGet(saved)))
	s.writeInvoiceDetail(w, r, invoiceRecordFromGet(saved))
}

// duplicateInvoiceReference reports whether err is a unique violation of the
// per-organization reference index — the check can lose a race with a
// concurrent insert, so the constraint is the authority.
func duplicateInvoiceReference(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" && pgErr.ConstraintName == "invoices_organization_id_reference_unique"
	}
	return false
}

// jsonFloat reads an optional JSON number.
func jsonFloat(m map[string]json.RawMessage, key string) (float64, bool, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, true, false
	}
	return f, true, true
}

// invoiceMoney renders minor units in the invoice currency.
func invoiceMoney(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%.2f %s", sign, float64(minor)/100, currency)
}
