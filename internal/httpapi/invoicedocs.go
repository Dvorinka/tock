package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Dvorinka/tock/internal/db"
)

// invoiceLine is a resolved line item with its extended amount.
type invoiceLine struct {
	Item  db.InvoiceItem
	Total int64
}

// invoiceTotals is the money breakdown of an invoice, in minor units.
type invoiceTotals struct {
	Lines     []invoiceLine
	Subtotal  int64
	Discount  int64
	Tax       int64
	Total     int64
	TaxRate   int32
	ReverseCh bool
}

func roundMinor(value float64) int64 {
	return int64(math.Round(value))
}

// invoiceTotalsFor computes line, discount, tax and payable amounts from the
// stored items. `discount_type: percentage` treats discount_amount as whole
// percent; `fixed` is an amount in minor units.
func invoiceTotalsFor(invoice invoiceRecord, items []db.InvoiceItem) invoiceTotals {
	totals := invoiceTotals{TaxRate: invoice.TaxRate, ReverseCh: invoice.IsEuReverseCharge}
	for _, item := range items {
		line := invoiceLine{Item: item, Total: roundMinor(float64(item.UnitPrice) * numericToFloat(item.Quantity))}
		totals.Subtotal += line.Total
		totals.Lines = append(totals.Lines, line)
	}
	if invoice.DiscountType == discountTypePercentage {
		totals.Discount = roundMinor(float64(totals.Subtotal) * float64(invoice.DiscountAmount) / 100)
	} else {
		totals.Discount = invoice.DiscountAmount
	}
	if totals.Discount > totals.Subtotal {
		totals.Discount = totals.Subtotal
	}
	taxable := totals.Subtotal - totals.Discount
	if !invoice.IsEuReverseCharge {
		totals.Tax = roundMinor(float64(taxable) * float64(invoice.TaxRate) / 100)
	}
	totals.Total = taxable + totals.Tax
	return totals
}

// invoiceDocumentFilename slugifies the reference for use in a download name.
func invoiceDocumentFilename(reference, ext string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, reference)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "invoice"
	}
	return fmt.Sprintf("%s_%s.%s", slug, time.Now().UTC().Format("2006-01-02_15-04-05"), ext)
}

// invoicePDFHTML renders the printable invoice document.
func invoicePDFHTML(invoice invoiceRecord, recipient recipientRecord, totals invoiceTotals) string {
	var b strings.Builder
	esc := html.EscapeString
	cur := esc(invoice.Currency)

	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><style>
body{font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:12px;color:#111827;margin:0}
h1{font-size:20px;margin:0 0 4px}
.muted{color:#6b7280}
.header{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:28px}
.grid{display:flex;gap:40px;margin-bottom:24px}
.grid>div{flex:1}
table{width:100%;border-collapse:collapse;margin:8px 0 16px}
th,td{text-align:left;padding:7px 6px;border-bottom:1px solid #e5e7eb;vertical-align:top}
th{font-size:10px;text-transform:uppercase;letter-spacing:.06em;color:#6b7280}
td.num,th.num{text-align:right;white-space:nowrap}
.totals{margin-left:auto;width:280px}
.totals td{border:0;padding:4px 6px}
.totals tr.grand td{border-top:1px solid #111827;font-weight:600;padding-top:8px}
.notes{margin-top:28px;padding-top:12px;border-top:1px solid #e5e7eb;white-space:pre-wrap}
</style></head><body>`)

	b.WriteString(`<div class="header"><div><h1>` + esc(invoice.SellerName) + `</h1>`)
	for _, line := range []*string{
		invoice.SellerAddressLine1, invoice.SellerAddressLine2, invoice.SellerAddressLine3,
	} {
		if line != nil && *line != "" {
			b.WriteString(`<div>` + esc(*line) + `</div>`)
		}
	}
	if invoice.SellerAddressPostCode != nil || invoice.SellerAddressCity != nil {
		b.WriteString(`<div>` + esc(deref(invoice.SellerAddressPostCode)) + ` ` + esc(deref(invoice.SellerAddressCity)) + `</div>`)
	}
	if invoice.SellerAddressCountry != nil && *invoice.SellerAddressCountry != "" {
		b.WriteString(`<div>` + esc(*invoice.SellerAddressCountry) + `</div>`)
	}
	if invoice.SellerVatin != nil && *invoice.SellerVatin != "" {
		b.WriteString(`<div class="muted">VAT ID: ` + esc(*invoice.SellerVatin) + `</div>`)
	}
	if invoice.SellerEmail != nil && *invoice.SellerEmail != "" {
		b.WriteString(`<div class="muted">` + esc(*invoice.SellerEmail) + `</div>`)
	}
	b.WriteString(`</div><div style="text-align:right"><h1>Invoice</h1>`)
	b.WriteString(`<div class="muted">` + esc(invoice.Reference) + `</div>`)
	b.WriteString(`<div>` + ymd(invoice.Date) + `</div>`)
	if invoice.DueAt.Valid {
		b.WriteString(`<div class="muted">Due ` + ymd(invoice.DueAt) + `</div>`)
	}
	b.WriteString(`</div></div>`)

	b.WriteString(`<div class="grid"><div><div class="muted">Billed to</div><strong>` + esc(recipient.Name) + `</strong>`)
	for _, line := range []*string{recipient.AddressLine1, recipient.AddressLine2, recipient.AddressLine3} {
		if line != nil && *line != "" {
			b.WriteString(`<div>` + esc(*line) + `</div>`)
		}
	}
	if recipient.AddressPostCode != nil || recipient.AddressCity != nil {
		b.WriteString(`<div>` + esc(deref(recipient.AddressPostCode)) + ` ` + esc(deref(recipient.AddressCity)) + `</div>`)
	}
	if recipient.AddressCountry != nil && *recipient.AddressCountry != "" {
		b.WriteString(`<div>` + esc(*recipient.AddressCountry) + `</div>`)
	}
	if recipient.Vatin != nil && *recipient.Vatin != "" {
		b.WriteString(`<div class="muted">VAT ID: ` + esc(*recipient.Vatin) + `</div>`)
	}
	b.WriteString(`</div><div>`)
	if invoice.BillingPeriodStart.Valid || invoice.BillingPeriodEnd.Valid {
		b.WriteString(`<div class="muted">Billing period</div><div>` + ymd(invoice.BillingPeriodStart) + ` – ` + ymd(invoice.BillingPeriodEnd) + `</div>`)
	}
	b.WriteString(`</div></div>`)

	b.WriteString(`<table><thead><tr><th>Description</th><th class="num">Qty</th><th class="num">Unit price</th><th class="num">Amount</th></tr></thead><tbody>`)
	for _, line := range totals.Lines {
		b.WriteString(`<tr><td><strong>` + esc(line.Item.Name) + `</strong>`)
		if line.Item.Description != nil && *line.Item.Description != "" {
			b.WriteString(`<div class="muted">` + esc(*line.Item.Description) + `</div>`)
		}
		b.WriteString(`</td><td class="num">` + formatQuantity(numericToFloat(line.Item.Quantity)) + `</td>`)
		b.WriteString(`<td class="num">` + invoiceMoney(line.Item.UnitPrice, cur) + `</td>`)
		b.WriteString(`<td class="num">` + invoiceMoney(line.Total, cur) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)

	b.WriteString(`<table class="totals">`)
	b.WriteString(`<tr><td>Subtotal</td><td class="num">` + invoiceMoney(totals.Subtotal, cur) + `</td></tr>`)
	if totals.Discount > 0 {
		label := "Discount"
		if invoice.DiscountType == discountTypePercentage {
			label = fmt.Sprintf("Discount (%d%%)", invoice.DiscountAmount)
		}
		b.WriteString(`<tr><td>` + label + `</td><td class="num">-` + invoiceMoney(totals.Discount, cur) + `</td></tr>`)
	}
	if totals.ReverseCh {
		b.WriteString(`<tr><td>VAT (reverse charge)</td><td class="num">` + invoiceMoney(0, cur) + `</td></tr>`)
	} else {
		fmt.Fprintf(&b, `<tr><td>VAT (%d%%)</td><td class="num">%s</td></tr>`, totals.TaxRate, invoiceMoney(totals.Tax, cur))
	}
	b.WriteString(`<tr class="grand"><td>Total</td><td class="num">` + invoiceMoney(totals.Total, cur) + `</td></tr>`)
	b.WriteString(`</table>`)

	if invoice.PaymentTerms != nil && *invoice.PaymentTerms != "" {
		b.WriteString(`<div class="notes">` + esc(*invoice.PaymentTerms) + `</div>`)
	}
	if invoice.PaymentIban != nil && *invoice.PaymentIban != "" {
		b.WriteString(`<div class="notes">Pay to IBAN ` + esc(*invoice.PaymentIban) + `</div>`)
	}
	if invoice.Notes != nil && *invoice.Notes != "" {
		b.WriteString(`<div class="notes">` + esc(*invoice.Notes) + `</div>`)
	}
	if invoice.Footer != nil && *invoice.Footer != "" {
		b.WriteString(`<div class="notes muted">` + esc(*invoice.Footer) + `</div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

// formatQuantity renders quantities without trailing zeros.
func formatQuantity(qty float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", qty), "0"), ".")
}

// invoiceUBLXML renders the EN 16931 (UBL 2.1) e-invoice document.
func invoiceUBLXML(invoice invoiceRecord, recipient recipientRecord, totals invoiceTotals) []byte {
	var b strings.Builder
	esc := html.EscapeString
	cur := esc(invoice.Currency)
	amount := func(minor int64) string { return fmt.Sprintf("%.2f", float64(minor)/100) }

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"`)
	b.WriteString(` xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"`)
	b.WriteString(` xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">`)
	b.WriteString(`<cbc:CustomizationID>urn:cen.eu:en16931:2017</cbc:CustomizationID>`)
	b.WriteString(`<cbc:ProfileID>urn:fdc:peppol.eu:2017:poacc:billing:01:1.0</cbc:ProfileID>`)
	b.WriteString(`<cbc:ID>` + esc(invoice.Reference) + `</cbc:ID>`)
	b.WriteString(`<cbc:IssueDate>` + ymd(invoice.Date) + `</cbc:IssueDate>`)
	if invoice.DueAt.Valid {
		b.WriteString(`<cbc:DueDate>` + ymd(invoice.DueAt) + `</cbc:DueDate>`)
	}
	b.WriteString(`<cbc:InvoiceTypeCode>380</cbc:InvoiceTypeCode>`)
	if invoice.Notes != nil && *invoice.Notes != "" {
		b.WriteString(`<cbc:Note>` + esc(*invoice.Notes) + `</cbc:Note>`)
	}
	b.WriteString(`<cbc:DocumentCurrencyCode>` + cur + `</cbc:DocumentCurrencyCode>`)
	if invoice.BillingPeriodStart.Valid && invoice.BillingPeriodEnd.Valid {
		b.WriteString(`<cac:InvoicePeriod><cbc:StartDate>` + ymd(invoice.BillingPeriodStart) +
			`</cbc:StartDate><cbc:EndDate>` + ymd(invoice.BillingPeriodEnd) + `</cbc:EndDate></cac:InvoicePeriod>`)
	}

	party := func(name string, vatin *string, line1, line2, line3, postCode, city, country *string, email *string) string {
		var p strings.Builder
		p.WriteString(`<cac:Party>`)
		p.WriteString(`<cac:PartyName><cbc:Name>` + esc(name) + `</cbc:Name></cac:PartyName>`)
		p.WriteString(`<cac:PostalAddress>`)
		for _, line := range []*string{line1, line2, line3} {
			if line != nil && *line != "" {
				p.WriteString(`<cbc:StreetName>` + esc(*line) + `</cbc:StreetName>`)
			}
		}
		if city != nil && *city != "" {
			p.WriteString(`<cbc:CityName>` + esc(*city) + `</cbc:CityName>`)
		}
		if postCode != nil && *postCode != "" {
			p.WriteString(`<cbc:PostalZone>` + esc(*postCode) + `</cbc:PostalZone>`)
		}
		if country != nil && *country != "" {
			p.WriteString(`<cac:Country><cbc:IdentificationCode>` + esc(*country) + `</cbc:IdentificationCode></cac:Country>`)
		}
		p.WriteString(`</cac:PostalAddress>`)
		if vatin != nil && *vatin != "" {
			p.WriteString(`<cac:PartyTaxScheme><cbc:CompanyID>` + esc(*vatin) + `</cbc:CompanyID>`)
			p.WriteString(`<cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:PartyTaxScheme>`)
		}
		if email != nil && *email != "" {
			p.WriteString(`<cac:Contact><cbc:ElectronicMail>` + esc(*email) + `</cbc:ElectronicMail></cac:Contact>`)
		}
		p.WriteString(`</cac:Party>`)
		return p.String()
	}

	b.WriteString(`<cac:AccountingSupplierParty>` + party(invoice.SellerName, invoice.SellerVatin,
		invoice.SellerAddressLine1, invoice.SellerAddressLine2, invoice.SellerAddressLine3,
		invoice.SellerAddressPostCode, invoice.SellerAddressCity, invoice.SellerAddressCountry,
		invoice.SellerEmail) + `</cac:AccountingSupplierParty>`)
	b.WriteString(`<cac:AccountingCustomerParty>` + party(recipient.Name, recipient.Vatin,
		recipient.AddressLine1, recipient.AddressLine2, recipient.AddressLine3,
		recipient.AddressPostCode, recipient.AddressCity, recipient.AddressCountry,
		recipient.Email) + `</cac:AccountingCustomerParty>`)

	b.WriteString(`<cac:TaxTotal><cbc:TaxAmount currencyID="` + cur + `">` + amount(totals.Tax) + `</cbc:TaxAmount>`)
	b.WriteString(`<cac:TaxSubtotal><cbc:TaxableAmount currencyID="` + cur + `">` + amount(totals.Subtotal-totals.Discount) + `</cbc:TaxableAmount>`)
	b.WriteString(`<cbc:TaxAmount currencyID="` + cur + `">` + amount(totals.Tax) + `</cbc:TaxAmount>`)
	b.WriteString(`<cac:TaxCategory><cbc:ID>` + taxCategoryID(totals) + `</cbc:ID><cbc:Percent>` +
		fmt.Sprintf("%d", totals.TaxRate) + `</cbc:Percent>`)
	b.WriteString(`<cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:TaxCategory></cac:TaxSubtotal></cac:TaxTotal>`)

	b.WriteString(`<cac:LegalMonetaryTotal>`)
	b.WriteString(`<cbc:LineExtensionAmount currencyID="` + cur + `">` + amount(totals.Subtotal) + `</cbc:LineExtensionAmount>`)
	b.WriteString(`<cbc:TaxExclusiveAmount currencyID="` + cur + `">` + amount(totals.Subtotal-totals.Discount) + `</cbc:TaxExclusiveAmount>`)
	b.WriteString(`<cbc:TaxInclusiveAmount currencyID="` + cur + `">` + amount(totals.Total) + `</cbc:TaxInclusiveAmount>`)
	if totals.Discount > 0 {
		b.WriteString(`<cbc:AllowanceTotalAmount currencyID="` + cur + `">` + amount(totals.Discount) + `</cbc:AllowanceTotalAmount>`)
	}
	b.WriteString(`<cbc:PayableAmount currencyID="` + cur + `">` + amount(totals.Total) + `</cbc:PayableAmount>`)
	b.WriteString(`</cac:LegalMonetaryTotal>`)

	for i, line := range totals.Lines {
		b.WriteString(`<cac:InvoiceLine><cbc:ID>` + fmt.Sprintf("%d", i+1) + `</cbc:ID>`)
		b.WriteString(`<cbc:InvoicedQuantity unitCode="C62">` + formatQuantity(numericToFloat(line.Item.Quantity)) + `</cbc:InvoicedQuantity>`)
		b.WriteString(`<cbc:LineExtensionAmount currencyID="` + cur + `">` + amount(line.Total) + `</cbc:LineExtensionAmount>`)
		b.WriteString(`<cac:Item><cbc:Name>` + esc(line.Item.Name) + `</cbc:Name>`)
		if line.Item.Description != nil && *line.Item.Description != "" {
			b.WriteString(`<cbc:Description>` + esc(*line.Item.Description) + `</cbc:Description>`)
		}
		b.WriteString(`<cac:ClassifiedTaxCategory><cbc:ID>` + taxCategoryID(totals) + `</cbc:ID><cbc:Percent>` +
			fmt.Sprintf("%d", totals.TaxRate) + `</cbc:Percent>`)
		b.WriteString(`<cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:ClassifiedTaxCategory></cac:Item>`)
		b.WriteString(`<cac:Price><cbc:PriceAmount currencyID="` + cur + `">` + amount(line.Item.UnitPrice) + `</cbc:PriceAmount></cac:Price>`)
		b.WriteString(`</cac:InvoiceLine>`)
	}
	b.WriteString(`</Invoice>`)
	return []byte(b.String())
}

// taxCategoryID follows the EN 16931 category codes: AE for reverse charge,
// S for a standard rated supply, Z for zero rated.
func taxCategoryID(totals invoiceTotals) string {
	switch {
	case totals.ReverseCh:
		return "AE"
	case totals.TaxRate == 0:
		return "Z"
	default:
		return "S"
	}
}

// invoiceDocuments loads everything a download needs.
func (s *Server) invoiceDocuments(r *http.Request, invoice invoiceRecord) (recipientRecord, []db.InvoiceItem, bool) {
	q := db.New(s.pool)
	recipient, err := q.GetInvoiceRecipient(r.Context(), invoice.InvoiceRecipientID)
	if err != nil {
		slog.ErrorContext(r.Context(), "load invoice recipient", "error", err)
		return recipientRecord{}, nil, false
	}
	items, err := q.ListInvoiceItems(r.Context(), invoice.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list invoice items", "error", err)
		return recipientRecord{}, nil, false
	}
	return recipientRecordFromGet(recipient), items, true
}

// handleDownloadInvoice: POST .../invoices/{invoice}/download
//
// Renders the PDF through Gotenberg; with_e_invoice bundles the UBL document
// into the same ZIP so the pair can be forwarded as one attachment.
func (s *Server) handleDownloadInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:download")
	if !ok {
		return
	}
	invoice, ok := s.scopedInvoice(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	withEInvoice := false
	if _, present := body["with_e_invoice"]; present {
		value, _, valid := jsonBool(body, "with_e_invoice")
		if !valid {
			validationErr(w, map[string][]string{"with_e_invoice": {"The with e invoice field must be true or false."}})
			return
		}
		withEInvoice = value
	}
	recipient, items, ok := s.invoiceDocuments(r, invoice)
	if !ok {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	totals := invoiceTotalsFor(invoice, items)
	pdf, err := s.gotenbergPDF(invoicePDFHTML(invoice, recipient, totals), "", nil, "")
	if err != nil {
		slog.ErrorContext(r.Context(), "render invoice pdf", "error", err)
		writeDomainErr(w, fmt.Errorf("pdf: %w", err))
		return
	}

	var filename string
	var content []byte
	if withEInvoice {
		filename = invoiceDocumentFilename(invoice.Reference, "zip")
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if entry, err := zw.Create("invoice.pdf"); err == nil {
			_, _ = entry.Write(pdf)
		}
		if entry, err := zw.Create("invoice.xml"); err == nil {
			_, _ = entry.Write(invoiceUBLXML(invoice, recipient, totals))
		}
		if err := zw.Close(); err != nil {
			slog.ErrorContext(r.Context(), "zip invoice documents", "error", err)
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		content = buf.Bytes()
	} else {
		filename = invoiceDocumentFilename(invoice.Reference, "pdf")
		content = pdf
	}
	s.writeDownloadLink(w, r, filename, content)
}

// handleDownloadEInvoice: POST .../invoices/{invoice}/download-e-invoice
func (s *Server) handleDownloadEInvoice(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invoices:download")
	if !ok {
		return
	}
	invoice, ok := s.scopedInvoice(w, r, scope)
	if !ok {
		return
	}
	recipient, items, ok := s.invoiceDocuments(r, invoice)
	if !ok {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	totals := invoiceTotalsFor(invoice, items)
	s.writeDownloadLink(w, r, invoiceDocumentFilename(invoice.Reference, "xml"),
		invoiceUBLXML(invoice, recipient, totals))
}

func (s *Server) writeDownloadLink(w http.ResponseWriter, r *http.Request, filename string, content []byte) {
	link, err := s.saveExportFile(filename, content)
	if err != nil {
		slog.ErrorContext(r.Context(), "save invoice document", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"download_link": link})
}
