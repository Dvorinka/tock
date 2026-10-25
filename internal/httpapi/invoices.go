package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// ---- Invoicing (phase 7, tock-owned) ----
//
// There is no upstream API to port: invoicing ships in a private solidtime
// extension. The contract implemented here is the one the SPA's generated
// client describes (`resources/js/packages/api/src/openapi.json.client.ts`),
// with Laravel-style validation and error shapes so it behaves like the rest
// of the API.

const (
	invoiceStatusDraft     = "draft"
	invoiceStatusSent      = "sent"
	invoiceStatusPaid      = "paid"
	invoiceStatusCancelled = "cancelled"

	discountTypePercentage = "percentage"
	discountTypeFixed      = "fixed"

	ymdLayout = "2006-01-02"
)

var invoiceStatusLabels = map[string]string{
	invoiceStatusDraft:     "Draft",
	invoiceStatusSent:      "Sent",
	invoiceStatusPaid:      "Paid",
	invoiceStatusCancelled: "Cancelled",
}

// ---- records ----

type recipientRecord struct {
	ID                  pgtype.UUID
	OrganizationID      pgtype.UUID
	Name                string
	Vatin               *string
	AddressLine1        *string
	AddressLine2        *string
	AddressLine3        *string
	AddressPostCode     *string
	AddressCity         *string
	AddressCountry      *string
	Phone               *string
	Email               *string
	ArchivedAt          pgtype.Timestamp
	CreatedAt           pgtype.Timestamp
	UpdatedAt           pgtype.Timestamp
	InvoicesCount       int64
	HasNonDraftInvoices bool
}

func recipientRecordFromGet(r db.GetInvoiceRecipientRow) recipientRecord {
	return recipientRecord{
		ID: r.ID, OrganizationID: r.OrganizationID, Name: r.Name, Vatin: r.Vatin,
		AddressLine1: r.AddressLine1, AddressLine2: r.AddressLine2, AddressLine3: r.AddressLine3,
		AddressPostCode: r.AddressPostCode, AddressCity: r.AddressCity,
		AddressCountry: r.AddressCountry, Phone: r.Phone, Email: r.Email,
		ArchivedAt: r.ArchivedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		InvoicesCount: r.InvoicesCount, HasNonDraftInvoices: r.HasNonDraftInvoices,
	}
}

func recipientRecordFromList(r db.ListInvoiceRecipientsRow) recipientRecord {
	return recipientRecordFromGet(db.GetInvoiceRecipientRow(r))
}

type invoiceRecord struct {
	ID                    pgtype.UUID
	OrganizationID        pgtype.UUID
	InvoiceRecipientID    pgtype.UUID
	Status                string
	Date                  pgtype.Date
	DueAt                 pgtype.Date
	PaidDate              pgtype.Date
	Reference             string
	SellerName            string
	SellerVatin           *string
	SellerAddressLine1    *string
	SellerAddressLine2    *string
	SellerAddressLine3    *string
	SellerAddressPostCode *string
	SellerAddressCity     *string
	SellerAddressCountry  *string
	SellerPhone           *string
	SellerEmail           *string
	Currency              string
	BillingPeriodStart    pgtype.Date
	BillingPeriodEnd      pgtype.Date
	TaxRate               int32
	DiscountAmount        int64
	DiscountType          string
	PaymentIban           *string
	Footer                *string
	Notes                 *string
	PaymentTerms          *string
	IsEuReverseCharge     bool
	CreatedAt             pgtype.Timestamp
	UpdatedAt             pgtype.Timestamp
	RecipientName         string
}

func invoiceRecordFromGet(r db.GetInvoiceRow) invoiceRecord {
	return invoiceRecord{
		ID: r.ID, OrganizationID: r.OrganizationID, InvoiceRecipientID: r.InvoiceRecipientID,
		Status: r.Status, Date: r.Date, DueAt: r.DueAt, PaidDate: r.PaidDate,
		Reference: r.Reference, SellerName: r.SellerName, SellerVatin: r.SellerVatin,
		SellerAddressLine1: r.SellerAddressLine1, SellerAddressLine2: r.SellerAddressLine2,
		SellerAddressLine3: r.SellerAddressLine3, SellerAddressPostCode: r.SellerAddressPostCode,
		SellerAddressCity: r.SellerAddressCity, SellerAddressCountry: r.SellerAddressCountry,
		SellerPhone: r.SellerPhone, SellerEmail: r.SellerEmail, Currency: r.Currency,
		BillingPeriodStart: r.BillingPeriodStart, BillingPeriodEnd: r.BillingPeriodEnd,
		TaxRate: r.TaxRate, DiscountAmount: r.DiscountAmount, DiscountType: r.DiscountType,
		PaymentIban: r.PaymentIban, Footer: r.Footer, Notes: r.Notes,
		PaymentTerms: r.PaymentTerms, IsEuReverseCharge: r.IsEuReverseCharge,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, RecipientName: r.RecipientName,
	}
}

func invoiceRecordFromList(r db.ListInvoicesPaginatedRow) invoiceRecord {
	return invoiceRecordFromGet(db.GetInvoiceRow(r))
}

// ---- resources ----

// nullableStr renders a nullable column as JSON null or the string.
func nullableStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// strOrBlank matches the resources whose columns are documented as plain
// strings rather than nullable (Laravel returns "" for a null attribute there).
func strOrBlank(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ymd(d pgtype.Date) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format(ymdLayout)
}

func invoiceRecipientResource(r recipientRecord) map[string]any {
	return map[string]any{
		"id":                     domain.UUIDString(r.ID),
		"organization_id":        domain.UUIDString(r.OrganizationID),
		"name":                   r.Name,
		"vatin":                  nullableStr(r.Vatin),
		"address_line_1":         nullableStr(r.AddressLine1),
		"address_line_2":         nullableStr(r.AddressLine2),
		"address_line_3":         nullableStr(r.AddressLine3),
		"address_post_code":      nullableStr(r.AddressPostCode),
		"address_city":           nullableStr(r.AddressCity),
		"address_country":        nullableStr(r.AddressCountry),
		"phone":                  nullableStr(r.Phone),
		"email":                  nullableStr(r.Email),
		"is_archived":            r.ArchivedAt.Valid,
		"archived_at":            tsFormat(r.ArchivedAt),
		"invoices_count":         r.InvoicesCount,
		"has_non_draft_invoices": r.HasNonDraftInvoices,
		"created_at":             tsFormat(r.CreatedAt),
		"updated_at":             tsFormat(r.UpdatedAt),
	}
}

func invoiceListResource(inv invoiceRecord) map[string]any {
	return map[string]any{
		"id":                   domain.UUIDString(inv.ID),
		"organization_id":      domain.UUIDString(inv.OrganizationID),
		"invoice_recipient_id": domain.UUIDString(inv.InvoiceRecipientID),
		"reference":            inv.Reference,
		"seller_name":          inv.SellerName,
		"recipient":            inv.RecipientName,
		"status":               inv.Status,
		"status_label":         invoiceStatusLabels[inv.Status],
		"date":                 ymd(inv.Date),
		"due_at":               ymd(inv.DueAt),
		"paid_date":            ymd(inv.PaidDate),
		"created_at":           tsFormat(inv.CreatedAt),
		"updated_at":           tsFormat(inv.UpdatedAt),
	}
}

func invoiceDetailedResource(inv invoiceRecord, recipient recipientRecord, items []db.InvoiceItem) map[string]any {
	entries := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entries = append(entries, map[string]any{
			"id":          domain.UUIDString(item.ID),
			"invoice_id":  domain.UUIDString(item.InvoiceID),
			"name":        item.Name,
			"description": nullableStr(item.Description),
			"unit_price":  item.UnitPrice,
			"quantity":    numericToFloat(item.Quantity),
			"order_index": item.OrderIndex,
			"created_at":  tsFormat(item.CreatedAt),
			"updated_at":  tsFormat(item.UpdatedAt),
		})
	}
	return map[string]any{
		"id":                       domain.UUIDString(inv.ID),
		"organization_id":          domain.UUIDString(inv.OrganizationID),
		"invoice_recipient_id":     domain.UUIDString(inv.InvoiceRecipientID),
		"reference":                inv.Reference,
		"seller_name":              inv.SellerName,
		"seller_vatin":             strOrBlank(inv.SellerVatin),
		"seller_address_line_1":    strOrBlank(inv.SellerAddressLine1),
		"seller_address_line_2":    strOrBlank(inv.SellerAddressLine2),
		"seller_address_line_3":    strOrBlank(inv.SellerAddressLine3),
		"seller_address_post_code": strOrBlank(inv.SellerAddressPostCode),
		"seller_address_city":      strOrBlank(inv.SellerAddressCity),
		"seller_address_country":   strOrBlank(inv.SellerAddressCountry),
		"seller_phone":             strOrBlank(inv.SellerPhone),
		"seller_email":             strOrBlank(inv.SellerEmail),
		"recipient":                invoiceRecipientResource(recipient),
		"paid_date":                ymd(inv.PaidDate),
		"due_at":                   ymd(inv.DueAt),
		"discount_type":            inv.DiscountType,
		"discount_amount":          inv.DiscountAmount,
		"tax_rate":                 inv.TaxRate,
		"payment_iban":             strOrBlank(inv.PaymentIban),
		"status":                   inv.Status,
		"currency":                 inv.Currency,
		"date":                     ymd(inv.Date),
		"footer":                   strOrBlank(inv.Footer),
		"notes":                    strOrBlank(inv.Notes),
		"payment_terms":            strOrBlank(inv.PaymentTerms),
		"is_eu_reverse_charge":     inv.IsEuReverseCharge,
		"billing_period_start":     ymd(inv.BillingPeriodStart),
		"billing_period_end":       ymd(inv.BillingPeriodEnd),
		"created_at":               tsFormat(inv.CreatedAt),
		"updated_at":               tsFormat(inv.UpdatedAt),
		"entries":                  entries,
	}
}

func invoiceSettingResource(setting db.InvoiceSetting) map[string]any {
	return map[string]any{
		"organization_id":          domain.UUIDString(setting.OrganizationID),
		"seller_name":              nullableStr(setting.SellerName),
		"seller_vatin":             nullableStr(setting.SellerVatin),
		"seller_address_line_1":    nullableStr(setting.SellerAddressLine1),
		"seller_address_line_2":    nullableStr(setting.SellerAddressLine2),
		"seller_address_line_3":    nullableStr(setting.SellerAddressLine3),
		"seller_address_post_code": nullableStr(setting.SellerAddressPostCode),
		"seller_address_city":      nullableStr(setting.SellerAddressCity),
		"seller_address_country":   nullableStr(setting.SellerAddressCountry),
		"seller_phone":             nullableStr(setting.SellerPhone),
		"seller_email":             nullableStr(setting.SellerEmail),
		"footer_default":           nullableStr(setting.FooterDefault),
		"notes_default":            nullableStr(setting.NotesDefault),
		"tax_rate_default":         setting.TaxRateDefault,
		"e_invoicing_enabled":      setting.EInvoicingEnabled,
	}
}

func numericToFloat(n pgtype.Numeric) float64 {
	v, err := n.Float64Value()
	if err != nil || !v.Valid {
		return 0
	}
	return v.Float64
}

func floatToNumeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', 4, 64))
	return n
}

// ---- invoice settings ----

// handleShowInvoiceSettings: GET /api/v1/organizations/{organization}/invoice-settings
//
// A missing row answers a zeroed resource so the settings form has something
// to bind to; it is created on the first update.
func (s *Server) handleShowInvoiceSettings(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-settings:view")
	if !ok {
		return
	}
	setting, err := db.New(s.pool).GetInvoiceSettings(r.Context(), scope.org.ID)
	if err != nil {
		setting = db.InvoiceSetting{OrganizationID: scope.org.ID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": invoiceSettingResource(setting)})
}

// handleUpdateInvoiceSettings: PUT /api/v1/organizations/{organization}/invoice-settings
func (s *Server) handleUpdateInvoiceSettings(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-settings:update")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	current, err := db.New(s.pool).GetInvoiceSettings(r.Context(), scope.org.ID)
	if err != nil {
		current = db.InvoiceSetting{OrganizationID: scope.org.ID}
	}
	errs := map[string][]string{}

	applyNullableString(body, errs, "seller_name", 255, &current.SellerName)
	applyNullableString(body, errs, "seller_vatin", 255, &current.SellerVatin)
	applyNullableString(body, errs, "seller_address_line_1", 255, &current.SellerAddressLine1)
	applyNullableString(body, errs, "seller_address_line_2", 255, &current.SellerAddressLine2)
	applyNullableString(body, errs, "seller_address_line_3", 255, &current.SellerAddressLine3)
	applyNullableString(body, errs, "seller_address_post_code", 255, &current.SellerAddressPostCode)
	applyNullableString(body, errs, "seller_address_city", 255, &current.SellerAddressCity)
	applyNullableString(body, errs, "seller_address_country", 255, &current.SellerAddressCountry)
	applyNullableString(body, errs, "seller_phone", 255, &current.SellerPhone)
	applyNullableString(body, errs, "seller_email", 255, &current.SellerEmail)
	applyNullableString(body, errs, "footer_default", 0, &current.FooterDefault)
	applyNullableString(body, errs, "notes_default", 0, &current.NotesDefault)
	applyNullableInt32(body, errs, "tax_rate_default", 0, 2147483647, &current.TaxRateDefault)
	if raw, present := body["e_invoicing_enabled"]; present {
		value, _, valid := jsonBool(body, "e_invoicing_enabled")
		if !valid {
			errs["e_invoicing_enabled"] = []string{"The e invoicing enabled field must be true or false."}
		} else {
			current.EInvoicingEnabled = value
		}
		_ = raw
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}

	setting, err := db.New(s.pool).UpsertInvoiceSettings(r.Context(), db.UpsertInvoiceSettingsParams{
		OrganizationID:        scope.org.ID,
		SellerName:            current.SellerName,
		SellerVatin:           current.SellerVatin,
		SellerAddressLine1:    current.SellerAddressLine1,
		SellerAddressLine2:    current.SellerAddressLine2,
		SellerAddressLine3:    current.SellerAddressLine3,
		SellerAddressPostCode: current.SellerAddressPostCode,
		SellerAddressCity:     current.SellerAddressCity,
		SellerAddressCountry:  current.SellerAddressCountry,
		SellerPhone:           current.SellerPhone,
		SellerEmail:           current.SellerEmail,
		FooterDefault:         current.FooterDefault,
		NotesDefault:          current.NotesDefault,
		TaxRateDefault:        current.TaxRateDefault,
		EInvoicingEnabled:     current.EInvoicingEnabled,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "update invoice settings", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": invoiceSettingResource(setting)})
}

// ---- invoice recipients ----

func (s *Server) scopedInvoiceRecipient(w http.ResponseWriter, r *http.Request, scope *orgScope) (recipientRecord, bool) {
	raw := chi.URLParam(r, "invoiceRecipient")
	id, ok := domain.UUID(raw)
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("InvoiceRecipient", raw))
		return recipientRecord{}, false
	}
	row, err := db.New(s.pool).GetInvoiceRecipient(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("InvoiceRecipient", raw))
		return recipientRecord{}, false
	}
	if row.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return recipientRecord{}, false
	}
	return recipientRecordFromGet(row), true
}

// handleListInvoiceRecipients: GET /api/v1/organizations/{organization}/invoice-recipients
func (s *Server) handleListInvoiceRecipients(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-recipients:view")
	if !ok {
		return
	}
	rows, err := db.New(s.pool).ListInvoiceRecipients(r.Context(), scope.org.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list invoice recipients", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, invoiceRecipientResource(recipientRecordFromList(row)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// handleShowInvoiceRecipient: GET .../invoice-recipients/{invoiceRecipient}
func (s *Server) handleShowInvoiceRecipient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-recipients:view")
	if !ok {
		return
	}
	recipient, ok := s.scopedInvoiceRecipient(w, r, scope)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": invoiceRecipientResource(recipient)})
}

// handleStoreInvoiceRecipient: POST .../invoice-recipients
func (s *Server) handleStoreInvoiceRecipient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-recipients:create")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	recipient, ok := s.buildInvoiceRecipient(w, r, scope, body, recipientRecord{})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": invoiceRecipientResource(recipient)})
}

// handleUpdateInvoiceRecipient: PUT .../invoice-recipients/{invoiceRecipient}
func (s *Server) handleUpdateInvoiceRecipient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-recipients:update")
	if !ok {
		return
	}
	current, ok := s.scopedInvoiceRecipient(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// The update request requires the same fields as the store request.
	recipient, ok := s.buildInvoiceRecipient(w, r, scope, body, current)
	if !ok {
		return
	}
	if err := db.New(s.pool).UpdateInvoiceRecipient(r.Context(), db.UpdateInvoiceRecipientParams{
		ID:              current.ID,
		Name:            recipient.Name,
		Vatin:           recipient.Vatin,
		AddressLine1:    recipient.AddressLine1,
		AddressLine2:    recipient.AddressLine2,
		AddressLine3:    recipient.AddressLine3,
		AddressPostCode: recipient.AddressPostCode,
		AddressCity:     recipient.AddressCity,
		AddressCountry:  recipient.AddressCountry,
		Phone:           recipient.Phone,
		Email:           recipient.Email,
		ArchivedAt:      recipient.ArchivedAt,
	}); err != nil {
		slog.ErrorContext(r.Context(), "update invoice recipient", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	updated, ok := s.scopedInvoiceRecipient(w, r, scope)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": invoiceRecipientResource(updated)})
}

// handleDuplicateInvoiceRecipient: POST .../invoice-recipients/{invoiceRecipient}/duplicate
//
// Copies the source recipient and applies the submitted fields on top.
func (s *Server) handleDuplicateInvoiceRecipient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-recipients:create")
	if !ok {
		return
	}
	source, ok := s.scopedInvoiceRecipient(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	source.ID = pgtype.UUID{}
	source.ArchivedAt = pgtype.Timestamp{}
	recipient, ok := s.buildInvoiceRecipient(w, r, scope, body, source)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": invoiceRecipientResource(recipient)})
}

// handleDestroyInvoiceRecipient: DELETE .../invoice-recipients/{invoiceRecipient}
func (s *Server) handleDestroyInvoiceRecipient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoice-recipients:delete")
	if !ok {
		return
	}
	recipient, ok := s.scopedInvoiceRecipient(w, r, scope)
	if !ok {
		return
	}
	if recipient.InvoicesCount > 0 {
		writeMessage(w, http.StatusConflict, "Invoice recipient is used by invoices.")
		return
	}
	if err := db.New(s.pool).DeleteInvoiceRecipient(r.Context(), recipient.ID); err != nil {
		slog.ErrorContext(r.Context(), "delete invoice recipient", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// buildInvoiceRecipient validates the request and inserts the recipient.
// `base` is the current state for updates and duplicates; a zero value for
// creates, which makes `name` required.
func (s *Server) buildInvoiceRecipient(w http.ResponseWriter, r *http.Request, scope *orgScope, body map[string]json.RawMessage, base recipientRecord) (recipientRecord, bool) {
	errs := map[string][]string{}
	recipient := base

	if value, present, isString := jsonStringField(body, "name"); present {
		switch {
		case !isString:
			errs["name"] = []string{"The name field must be a string."}
		case value == "":
			errs["name"] = []string{"The name field is required."}
		case len(value) > 255:
			errs["name"] = []string{"The name field must not be greater than 255 characters."}
		default:
			recipient.Name = value
		}
	} else if base.Name == "" {
		errs["name"] = []string{"The name field is required."}
	}

	applyNullableString(body, errs, "vatin", 255, &recipient.Vatin)
	applyNullableString(body, errs, "address_line_1", 255, &recipient.AddressLine1)
	applyNullableString(body, errs, "address_line_2", 255, &recipient.AddressLine2)
	applyNullableString(body, errs, "address_line_3", 255, &recipient.AddressLine3)
	applyNullableString(body, errs, "address_post_code", 255, &recipient.AddressPostCode)
	applyNullableString(body, errs, "address_city", 255, &recipient.AddressCity)
	applyNullableString(body, errs, "address_country", 255, &recipient.AddressCountry)
	applyNullableString(body, errs, "phone", 255, &recipient.Phone)
	applyNullableString(body, errs, "email", 255, &recipient.Email)
	if raw, present := body["is_archived"]; present {
		value, _, valid := jsonBool(body, "is_archived")
		if !valid {
			errs["is_archived"] = []string{"The is archived field must be true or false."}
		} else if value && !recipient.ArchivedAt.Valid {
			recipient.ArchivedAt = pgtype.Timestamp{Time: time.Now().UTC(), Valid: true}
		} else if !value {
			recipient.ArchivedAt = pgtype.Timestamp{}
		}
		_ = raw
	}

	if len(errs) > 0 {
		validationErr(w, errs)
		return recipientRecord{}, false
	}

	id := pgUUID()
	if err := db.New(s.pool).InsertInvoiceRecipient(r.Context(), db.InsertInvoiceRecipientParams{
		ID:              id,
		OrganizationID:  scope.org.ID,
		Name:            recipient.Name,
		Vatin:           recipient.Vatin,
		AddressLine1:    recipient.AddressLine1,
		AddressLine2:    recipient.AddressLine2,
		AddressLine3:    recipient.AddressLine3,
		AddressPostCode: recipient.AddressPostCode,
		AddressCity:     recipient.AddressCity,
		AddressCountry:  recipient.AddressCountry,
		Phone:           recipient.Phone,
		Email:           recipient.Email,
		ArchivedAt:      recipient.ArchivedAt,
	}); err != nil {
		slog.ErrorContext(r.Context(), "insert invoice recipient", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return recipientRecord{}, false
	}
	row, err := db.New(s.pool).GetInvoiceRecipient(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "reload invoice recipient", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return recipientRecord{}, false
	}
	return recipientRecordFromGet(row), true
}

// ---- shared validation helpers ----

// jsonStringField distinguishes absent, null and a non-string value.
func jsonStringField(body map[string]json.RawMessage, key string) (string, bool, bool) {
	raw, present := body[key]
	if !present {
		return "", false, false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, false
	}
	return value, true, true
}

// applyNullableString validates an optional string field: absent leaves the
// target untouched, null clears it, a string sets it.
func applyNullableString(body map[string]json.RawMessage, errs map[string][]string, key string, maxLen int, target **string) {
	raw, present := body[key]
	if !present {
		return
	}
	label := strings.ReplaceAll(key, "_", " ")
	if string(raw) == "null" {
		*target = nil
		return
	}
	value, _, isString := jsonStringField(body, key)
	if !isString {
		errs[key] = []string{"The " + label + " field must be a string."}
		return
	}
	if maxLen > 0 && len(value) > maxLen {
		errs[key] = []string{fmt.Sprintf("The %s field must not be greater than %d characters.", label, maxLen)}
		return
	}
	*target = &value
}

func applyNullableInt32(body map[string]json.RawMessage, errs map[string][]string, key string, min, max int64, target **int32) {
	raw, present := body[key]
	if !present {
		return
	}
	label := strings.ReplaceAll(key, "_", " ")
	if string(raw) == "null" {
		*target = nil
		return
	}
	value, _, valid := jsonInt(body, key)
	switch {
	case !valid:
		errs[key] = []string{"The " + label + " field must be an integer."}
	case value < min:
		errs[key] = []string{fmt.Sprintf("The %s field must be at least %d.", label, min)}
	case value > max:
		errs[key] = []string{fmt.Sprintf("The %s field must not be greater than %d.", label, max)}
	default:
		v := int32(value)
		*target = &v
	}
}
