package httpapi

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

func testDate(t *testing.T, raw string) pgtype.Date {
	t.Helper()
	d, _, _, valid := jsonDateField(map[string]json.RawMessage{"d": json.RawMessage(`"` + raw + `"`)}, "d")
	if !valid {
		t.Fatalf("bad test date %q", raw)
	}
	return d
}

func invoiceFixture(t *testing.T) invoiceRecord {
	t.Helper()
	orgID := testOrgUUID(t)
	recipientID, _ := domain.UUID("0190abcd-1234-7000-8000-0000000000bb")
	return invoiceRecord{
		OrganizationID:     orgID,
		InvoiceRecipientID: recipientID,
		Status:             invoiceStatusDraft,
		Date:               testDate(t, "2026-03-01"),
		DueAt:              testDate(t, "2026-03-15"),
		Reference:          "2026-0001",
		SellerName:         "Tock Labs",
		Currency:           "EUR",
		TaxRate:            20,
		DiscountType:       discountTypeFixed,
	}
}

func invoiceItemFixture(name string, unitPrice int64, quantity float64) db.InvoiceItem {
	return db.InvoiceItem{Name: name, UnitPrice: unitPrice, Quantity: floatToNumeric(quantity)}
}

func TestInvoiceTotalsFixedDiscount(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.DiscountAmount = 5000
	totals := invoiceTotalsFor(invoice, []db.InvoiceItem{
		invoiceItemFixture("Development", 12000, 2.5), // 300.00
		invoiceItemFixture("Hosting", 2000, 1),        // 20.00
	})
	if totals.Subtotal != 32000 {
		t.Fatalf("subtotal = %d, want 32000", totals.Subtotal)
	}
	if totals.Discount != 5000 {
		t.Fatalf("discount = %d, want 5000", totals.Discount)
	}
	if totals.Tax != 5400 { // (32000-5000) * 20%
		t.Fatalf("tax = %d, want 5400", totals.Tax)
	}
	if totals.Total != 32400 {
		t.Fatalf("total = %d, want 32400", totals.Total)
	}
}

func TestInvoiceTotalsPercentageDiscount(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.DiscountType = discountTypePercentage
	invoice.DiscountAmount = 10
	totals := invoiceTotalsFor(invoice, []db.InvoiceItem{invoiceItemFixture("Retainer", 100000, 1)})
	if totals.Discount != 10000 {
		t.Fatalf("discount = %d, want 10000", totals.Discount)
	}
	if totals.Tax != 18000 {
		t.Fatalf("tax = %d, want 18000", totals.Tax)
	}
	if totals.Total != 108000 {
		t.Fatalf("total = %d, want 108000", totals.Total)
	}
}

// A discount larger than the subtotal is clamped: the invoice never goes
// negative and VAT is computed on zero.
func TestInvoiceTotalsDiscountClamped(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.DiscountAmount = 999999
	totals := invoiceTotalsFor(invoice, []db.InvoiceItem{invoiceItemFixture("Retainer", 10000, 1)})
	if totals.Discount != 10000 {
		t.Fatalf("discount = %d, want 10000", totals.Discount)
	}
	if totals.Tax != 0 || totals.Total != 0 {
		t.Fatalf("tax/total = %d/%d, want 0/0", totals.Tax, totals.Total)
	}
}

// EU reverse charge: no VAT is charged but the rate stays on the document.
func TestInvoiceTotalsReverseCharge(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.IsEuReverseCharge = true
	totals := invoiceTotalsFor(invoice, []db.InvoiceItem{invoiceItemFixture("Consulting", 50000, 1)})
	if totals.Tax != 0 {
		t.Fatalf("tax = %d, want 0", totals.Tax)
	}
	if totals.Total != 50000 {
		t.Fatalf("total = %d, want 50000", totals.Total)
	}
	if got := taxCategoryID(totals); got != "AE" {
		t.Fatalf("tax category = %q, want AE", got)
	}
}

func TestTaxCategoryID(t *testing.T) {
	if got := taxCategoryID(invoiceTotals{}); got != "Z" {
		t.Fatalf("zero rate category = %q, want Z", got)
	}
	if got := taxCategoryID(invoiceTotals{TaxRate: 20}); got != "S" {
		t.Fatalf("standard rate category = %q, want S", got)
	}
}

func TestFormatQuantityAndMoney(t *testing.T) {
	cases := map[float64]string{1: "1", 2.5: "2.5", 0.25: "0.25", 12.3456: "12.3456", 0: "0"}
	for qty, want := range cases {
		if got := formatQuantity(qty); got != want {
			t.Fatalf("formatQuantity(%v) = %q, want %q", qty, got, want)
		}
	}
	if got := invoiceMoney(123456, "EUR"); got != "1234.56 EUR" {
		t.Fatalf("invoiceMoney = %q", got)
	}
	if got := invoiceMoney(-500, "EUR"); got != "-5.00 EUR" {
		t.Fatalf("invoiceMoney negative = %q", got)
	}
}

func TestInvoiceDocumentFilename(t *testing.T) {
	got := invoiceDocumentFilename("2026/0001 A", "pdf")
	if strings.ContainsAny(strings.TrimSuffix(got, ".pdf"), "/ ") {
		t.Fatalf("filename keeps unsafe characters: %q", got)
	}
	if !strings.HasSuffix(got, ".pdf") || !strings.HasPrefix(got, "2026-0001-A_") {
		t.Fatalf("unexpected filename %q", got)
	}
	if got := invoiceDocumentFilename("", "xml"); !strings.HasPrefix(got, "invoice_") {
		t.Fatalf("empty reference should fall back, got %q", got)
	}
}

func TestInvoicePDFHTMLEscapesAndRenders(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.SellerName = "Tock <Labs>"
	recipient := recipientRecord{Name: "Acme & Co"}
	totals := invoiceTotalsFor(invoice, []db.InvoiceItem{invoiceItemFixture("Dev", 10000, 1)})
	page := invoicePDFHTML(invoice, recipient, totals)
	if strings.Contains(page, "<Labs>") {
		t.Fatal("seller name was not escaped")
	}
	for _, want := range []string{"Tock &lt;Labs&gt;", "Acme &amp; Co", "2026-0001", "100.00 EUR", "Invoice"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
}

func TestInvoiceUBLXMLIsWellFormed(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.IsEuReverseCharge = true
	recipient := recipientRecord{Name: "Acme & Co", AddressCountry: ptr("AT")}
	items := []db.InvoiceItem{invoiceItemFixture("Dev", 10000, 2)}
	doc := invoiceUBLXML(invoice, recipient, invoiceTotalsFor(invoice, items))

	decoder := xml.NewDecoder(strings.NewReader(string(doc)))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML: %v", err)
		}
	}
	for _, want := range []string{
		"urn:cen.eu:en16931:2017",
		"<cbc:ID>2026-0001</cbc:ID>",
		"<cbc:DocumentCurrencyCode>EUR</cbc:DocumentCurrencyCode>",
		"Acme &amp; Co",
		"<cbc:ID>AE</cbc:ID>",
		"<cbc:PayableAmount currencyID=\"EUR\">200.00</cbc:PayableAmount>",
	} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("e-invoice is missing %q", want)
		}
	}
}

func TestJsonDateField(t *testing.T) {
	body := map[string]json.RawMessage{
		"date":     json.RawMessage(`"2026-03-01"`),
		"null":     json.RawMessage(`null`),
		"bad":      json.RawMessage(`"not-a-date"`),
		"datetime": json.RawMessage(`"2026-03-01T10:00:00Z"`),
	}
	value, present, isNull, ok := jsonDateField(body, "date")
	if !present || isNull || !ok || ymd(value) != "2026-03-01" {
		t.Fatalf("plain date parsed wrong: %v %v %v %v", value, present, isNull, ok)
	}
	if _, present, isNull, ok := jsonDateField(body, "null"); !present || !isNull || !ok {
		t.Fatal("JSON null should be a present, valid null")
	}
	if _, present, _, ok := jsonDateField(body, "bad"); !present || ok {
		t.Fatal("invalid date should not parse")
	}
	if value, _, _, ok := jsonDateField(body, "datetime"); !ok || ymd(value) != "2026-03-01" {
		t.Fatal("RFC3339 timestamp should parse")
	}
	if _, present, _, _ := jsonDateField(body, "missing"); present {
		t.Fatal("absent key must not be reported as present")
	}
}

func TestParseInvoiceEntries(t *testing.T) {
	errs := map[string][]string{}
	entries := parseInvoiceEntries(json.RawMessage(`[
		{"name":"Dev","unit_price":12000,"quantity":2.5},
		{"name":"","unit_price":-1,"quantity":-2}
	]`), errs)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Name != "Dev" || entries[0].UnitPrice != 12000 || entries[0].Quantity != 2.5 {
		t.Fatalf("first entry parsed wrong: %+v", entries[0])
	}
	if entries[0].OrderIndex != 0 || entries[1].OrderIndex != 1 {
		t.Fatal("order_index should follow the array position")
	}
	for _, key := range []string{"entries.1.name", "entries.1.unit_price", "entries.1.quantity"} {
		if len(errs[key]) == 0 {
			t.Fatalf("missing validation error for %s: %v", key, errs)
		}
	}
	// Laravel renders wildcard attributes verbatim.
	if got := errs["entries.1.unit_price"][0]; got != "The entries.1.unit_price field must be at least 0." {
		t.Fatalf("unexpected message %q", got)
	}

	errs = map[string][]string{}
	if entries := parseInvoiceEntries(json.RawMessage(`{"name":"Dev"}`), errs); entries != nil {
		t.Fatal("a non-array should yield no entries")
	}
	if got := errs["entries"][0]; got != "The entries field must be an array." {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestInvoiceResources(t *testing.T) {
	invoice := invoiceFixture(t)
	invoice.PaidDate = pgtype.Date{}
	invoice.SellerVatin = nil
	recipient := recipientRecord{
		OrganizationID: invoice.OrganizationID,
		Name:           "Acme",
		ArchivedAt:     pgtype.Timestamp{Time: testDate(t, "2026-01-02").Time, Valid: true},
		InvoicesCount:  3,
	}

	list := invoiceListResource(invoice)
	if list["recipient"] != "" {
		t.Fatalf("list recipient = %v, want the joined name (empty here)", list["recipient"])
	}
	if list["status_label"] != "Draft" {
		t.Fatalf("status_label = %v", list["status_label"])
	}
	if list["date"] != "2026-03-01" || list["paid_date"] != "" {
		t.Fatalf("dates rendered wrong: %v / %v", list["date"], list["paid_date"])
	}

	detail := invoiceDetailedResource(invoice, recipient, nil)
	if detail["seller_vatin"] != "" {
		t.Fatalf("null seller_vatin should render as an empty string, got %v", detail["seller_vatin"])
	}
	if detail["is_eu_reverse_charge"] != false {
		t.Fatalf("reverse charge = %v", detail["is_eu_reverse_charge"])
	}
	if entries, ok := detail["entries"].([]map[string]any); !ok || len(entries) != 0 {
		t.Fatalf("entries = %v, want an empty array", detail["entries"])
	}

	resource := invoiceRecipientResource(recipient)
	if resource["is_archived"] != true || resource["archived_at"] == nil {
		t.Fatalf("archive flags rendered wrong: %+v", resource)
	}
	if resource["vatin"] != nil {
		t.Fatalf("null vatin should stay null on the recipient resource, got %v", resource["vatin"])
	}
	if resource["invoices_count"] != int64(3) {
		t.Fatalf("invoices_count = %v", resource["invoices_count"])
	}
}

func ptr[T any](v T) *T { return &v }
