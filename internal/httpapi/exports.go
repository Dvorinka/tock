package httpapi

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Dvorinka/tock/internal/db"
)

//go:embed pdfassets/outfit.ttf
var outfitTTF []byte

//go:embed pdfassets/echarts.min.js
var echartsJS []byte

// aggregationDescription ports TimeEntryAggregationType::description()
// (LaravelEnumHelper humanize fallback — no enum translations exist upstream).
func aggregationDescription(group string) string {
	switch group {
	case "day":
		return "Day"
	case "week":
		return "Week"
	case "month":
		return "Month"
	case "year":
		return "Year"
	case "user":
		return "User"
	case "project":
		return "Project"
	case "task":
		return "Task"
	case "client":
		return "Client"
	case "billable":
		return "Billable"
	case "description":
		return "Description"
	case "tag":
		return "Tag"
	case "type":
		return "Type"
	default:
		return group
	}
}

// colorByKey ports ColorService::getRandomColor with the seeded branch —
// deterministic pick keyed by crc32 of the group key.
var groupColors = []string{
	"#ef5350", "#ec407a", "#ab47bc", "#7e57c2", "#5c6bc0", "#42a5f5",
	"#29b6f6", "#26c6da", "#26a69a", "#66bb6a", "#9ccc65", "#d4e157",
	"#ffee58", "#ffca28", "#ffa726", "#ff7043", "#8d6e63", "#bdbdbd",
	"#78909c",
}

func colorByKey(key *string) string {
	if key == nil {
		return "#CCCCCC"
	}
	var sum uint32
	for _, b := range []byte(*key) {
		sum = sum*31 + uint32(b) // crc32-seeded array_rand ≈ stable index
	}
	return groupColors[sum%uint32(len(groupColors))]
}

// ---- shared row builders ----

var detailedHeader = []string{
	"Description", "Task", "Project", "Client", "User", "Start", "End",
	"Duration", "Duration (decimal)", "Billable", "Break", "Tags",
}

// detailedRow carries typed values so csv/xlsx/ods share one builder.
type detailedRow struct {
	description, task, project, client, user string
	start, end                               time.Time
	hasEnd                                   bool
	duration                                 int64
	billable, isBreak                        bool
	tags                                     string
}

func buildDetailedRows(entries []db.TimeEntry, names exportNameMaps, tz *time.Location) []detailedRow {
	rows := make([]detailedRow, 0, len(entries))
	for _, e := range entries {
		tagNames := []string{}
		var tagIDs []string
		_ = json.Unmarshal(e.Tags, &tagIDs)
		for _, t := range tagIDs {
			if n, ok := names.tags[t]; ok {
				tagNames = append(tagNames, n)
			}
		}
		r := detailedRow{
			description: e.Description,
			task:        names.tasks[uuidStr(e.TaskID)],
			project:     names.projects[uuidStr(e.ProjectID)],
			client:      names.clients[uuidStr(e.ClientID)],
			user:        names.users[uuidStr(e.UserID)],
			start:       e.Start.Time.In(tz),
			billable:    e.Billable,
			isBreak:     e.Type == timeEntryTypeBreak,
			tags:        strings.Join(tagNames, ", "),
		}
		if e.End.Valid {
			r.end = e.End.Time.In(tz)
			r.hasEnd = true
			r.duration = int64(e.End.Time.Sub(e.Start.Time).Seconds())
		}
		rows = append(rows, r)
	}
	return rows
}

// csvCells formats one detailed row for CSV/ODS. iv is the interval
// formatter: CSV uses IntervalService::format (always H:MM:SS); XLSX/ODS use
// LocalizationService::formatInterval (org setting).
func (r detailedRow) csvCells(l localization, iv func(int64) string) []string {
	end := ""
	dur := ""
	durDec := ""
	if r.hasEnd {
		end = r.end.Format("2006-01-02 15:04:05")
		dur = iv(r.duration)
		durDec = strconv.FormatFloat(float64(r.duration)/3600, 'f', -1, 64)
	}
	return []string{
		r.description, r.task, r.project, r.client, r.user,
		r.start.Format("2006-01-02 15:04:05"), end, dur, durDec,
		yesNo(r.billable), yesNo(r.isBreak), r.tags,
	}
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// aggExportRow is one g1×g2 line of the aggregate export table.
type aggExportRow struct {
	g1Label string
	g2Label string
	seconds int64
	cost    int64
}

// aggCellLabel resolves one group cell to its display label, matching the
// spreadsheet blade: description ?? formatTimeGroupKey(key) ?? '-'
// (billable → Yes/No).
func aggCellLabel(item map[string]any, groupType string, l localization) string {
	key := groupKey(item)
	if groupType == "billable" {
		if key == "1" {
			return "Yes"
		}
		return "No"
	}
	if d := groupDescription(item); d != "" {
		return d
	}
	if v := l.formatTimeGroupKey(keyOrNil(key), groupType); v != nil && *v != "" {
		return *v
	}
	return "-"
}

// keyOrNil turns an empty group key into nil, matching keyOrNull's JSON shape.
func keyOrNil(k string) *string {
	if k == "" {
		return nil
	}
	return &k
}

// groupKey reads a group key as a plain string (null keys become "").
func groupKey(item map[string]any) string {
	switch v := item["key"].(type) {
	case string:
		return v
	case *string:
		if v != nil {
			return *v
		}
	}
	return ""
}

// groupDescription reads a resolved description, when the aggregation attached one.
func groupDescription(item map[string]any) string {
	switch v := item["description"].(type) {
	case string:
		return v
	case *string:
		if v != nil {
			return *v
		}
	}
	return ""
}

// aggCellLabelPDF is the PDF variant — null keys render 'No <group>'.
func aggCellLabelPDF(item map[string]any, groupType string, l localization, upper bool) string {
	key := groupKey(item)
	if groupType == "billable" {
		if key == "1" {
			return "Billable"
		}
		return "Non-billable"
	}
	if d := groupDescription(item); d != "" {
		return d
	}
	if v := l.formatTimeGroupKey(keyOrNil(key), groupType); v != nil && *v != "" {
		return *v
	}
	label := aggregationDescription(groupType)
	if upper {
		return "No " + strings.ToLower(label)
	}
	return "-"
}

func buildAggRows(result map[string]any, group, subGroup string, l localization) []aggExportRow {
	gd, _ := result["grouped_data"].([]map[string]any)
	rows := []aggExportRow{}
	for _, g1 := range gd {
		sub, _ := g1["grouped_data"].([]map[string]any)
		for _, g2 := range sub {
			rows = append(rows, aggExportRow{
				g1Label: aggCellLabel(g1, group, l),
				g2Label: aggCellLabel(g2, subGroup, l),
				seconds: intSeconds(g2["seconds"]),
				cost:    intCost(g2["cost"]),
			})
		}
	}
	return rows
}

func intSeconds(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func intCost(v any) int64 { return intSeconds(v) }

// aggExportHeader is the spreadsheet blade <thead> row.
func aggExportHeader(group, subGroup, currency string) []string {
	return []string{
		aggregationDescription(group),
		aggregationDescription(subGroup),
		"Duration",
		"Duration (decimal)",
		"Amount (" + strings.ToUpper(currency) + ")",
	}
}

// ---- writers ----

// writeXLSX produces the Laravel-Excel style sheet: bold header, typed cells.
func writeXLSX(header []string, writeBody func(f *excelize.File)) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, err
	}
	for i, h := range header {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
		_ = f.SetCellStyle(sheet, cell, cell, bold)
	}
	writeBody(f)
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeDetailedXLSX(rows []detailedRow, l localization) ([]byte, error) {
	return writeXLSX(detailedHeader, func(f *excelize.File) {
		sheet := f.GetSheetName(0)
		dtStyle, _ := f.NewStyle(&excelize.Style{CustomNumFmt: strptr("yyyy-mm-dd hh:mm:ss")})
		numStyle, _ := f.NewStyle(&excelize.Style{NumFmt: 2}) // 0.00
		for i, r := range rows {
			row := i + 2
			set := func(col int, v any, style int) {
				cell, _ := excelize.CoordinatesToCellName(col, row)
				_ = f.SetCellValue(sheet, cell, v)
				if style != 0 {
					_ = f.SetCellStyle(sheet, cell, cell, style)
				}
			}
			set(1, r.description, 0)
			set(2, r.task, 0)
			set(3, r.project, 0)
			set(4, r.client, 0)
			set(5, r.user, 0)
			set(6, r.start, dtStyle)
			if r.hasEnd {
				set(7, r.end, dtStyle)
				set(8, l.formatInterval(r.duration), 0)
				set(9, float64(r.duration)/3600, numStyle)
			}
			set(10, yesNo(r.billable), 0)
			set(11, yesNo(r.isBreak), 0)
			set(12, r.tags, 0)
		}
	})
}

// writeAggXLSX mirrors the upstream spreadsheet blade in the XLSX branch:
// every cell has a 1px black border; duration is a numeric Excel-day value
// with [hh]:mm:ss; totals are SUM formulas (E column even without billable,
// matching upstream's unconditional emit).
func writeAggXLSX(rows []aggExportRow, group, subGroup, currency string, showBillable bool, l localization) ([]byte, error) {
	header := aggExportHeader(group, subGroup, currency)
	return writeXLSXBordered(header, func(f *excelize.File, st *borderStyles) {
		sheet := f.GetSheetName(0)
		for i, r := range rows {
			row := i + 2
			set := func(col int, v any, style int) {
				cell, _ := excelize.CoordinatesToCellName(col, row)
				_ = f.SetCellValue(sheet, cell, v)
				_ = f.SetCellStyle(sheet, cell, cell, style)
			}
			set(1, r.g1Label, st.plain)
			set(2, r.g2Label, st.plain)
			set(3, float64(r.seconds)/86400, st.dur) // Excel time fraction
			set(4, float64(r.seconds)/3600, st.num)
			if showBillable {
				set(5, float64(r.cost)/100, st.cost)
			}
		}
		// Total row — data-type=formula in the blade; counter>1 ? SUM : =0.
		total := len(rows) + 2
		setf := func(col int, formula string, style int) {
			cell, _ := excelize.CoordinatesToCellName(col, total)
			_ = f.SetCellFormula(sheet, cell, formula)
			_ = f.SetCellStyle(sheet, cell, cell, style)
		}
		set := func(col int, v any, style int) {
			cell, _ := excelize.CoordinatesToCellName(col, total)
			_ = f.SetCellValue(sheet, cell, v)
			_ = f.SetCellStyle(sheet, cell, cell, style)
		}
		set(1, "", st.bold)
		set(2, "Total", st.bold)
		var c, d, e string
		if len(rows) > 0 {
			last := len(rows) + 1
			c, d, e = fmt.Sprintf("SUM(C2:C%d)", last), fmt.Sprintf("SUM(D2:D%d)", last), fmt.Sprintf("SUM(E2:E%d)", last)
		} else {
			c, d, e = "0", "0", "0"
		}
		setf(3, c, st.boldDur)
		setf(4, d, st.boldNum)
		setf(5, e, st.boldCost)
	})
}

// borderStyles bundles the bordered style variants the report table needs —
// upstream gives every td/th `border: 1px solid black`.
type borderStyles struct {
	plain, dur, num, cost            int
	bold, boldDur, boldNum, boldCost int
}

var thinBorder = []excelize.Border{
	{Type: "left", Color: "000000", Style: 1},
	{Type: "top", Color: "000000", Style: 1},
	{Type: "right", Color: "000000", Style: 1},
	{Type: "bottom", Color: "000000", Style: 1},
}

func writeXLSXBordered(header []string, writeBody func(f *excelize.File, st *borderStyles)) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	mk := func(bold bool, nf int, custom string) int {
		st := &excelize.Style{Border: thinBorder}
		if bold {
			st.Font = &excelize.Font{Bold: true}
		}
		if custom != "" {
			st.CustomNumFmt = &custom
		} else if nf != 0 {
			st.NumFmt = nf
		}
		id, _ := f.NewStyle(st)
		return id
	}
	st := &borderStyles{
		plain:    mk(false, 0, ""),
		dur:      mk(false, 0, "[hh]:mm:ss"),
		num:      mk(false, 2, ""), // 0.00
		cost:     mk(false, 3, ""), // #,##0.00
		bold:     mk(true, 0, ""),
		boldDur:  mk(true, 0, "[hh]:mm:ss"),
		boldNum:  mk(true, 2, ""),
		boldCost: mk(true, 3, ""),
	}
	for i, h := range header {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
		_ = f.SetCellStyle(sheet, cell, cell, st.bold)
	}
	writeBody(f, st)
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func strptr(s string) *string { return &s }

// writeAggODS produces a minimal valid ODS 1.2 package with the same table —
// upstream ODS is the spreadsheet view rendered by PhpSpreadsheet.
func writeAggODS(rows []aggExportRow, group, subGroup, currency string, showBillable bool, l localization) ([]byte, error) {
	header := aggExportHeader(group, subGroup, currency)
	var b strings.Builder
	cell := func(v string) {
		b.WriteString(`<table:table-cell office:value-type="string"><text:p>`)
		b.WriteString(template.HTMLEscapeString(v))
		b.WriteString(`</text:p></table:table-cell>`)
	}
	b.WriteString(`<table:table table:name="Sheet1"><table:table-row>`)
	for _, h := range header {
		cell(h)
	}
	b.WriteString(`</table:table-row>`)
	var totalSec, totalCost int64
	for _, r := range rows {
		b.WriteString(`<table:table-row>`)
		cell(r.g1Label)
		cell(r.g2Label)
		cell(formatInterval(r.seconds))
		cell(strconv.FormatFloat(round2(float64(r.seconds)/3600), 'f', -1, 64))
		if showBillable {
			cell(strconv.FormatFloat(round2(float64(r.cost)/100), 'f', -1, 64))
		}
		b.WriteString(`</table:table-row>`)
		totalSec += r.seconds
		totalCost += r.cost
	}
	b.WriteString(`<table:table-row>`)
	cell("")
	cell("Total")
	cell(formatInterval(totalSec))
	cell(strconv.FormatFloat(round2(float64(totalSec)/3600), 'f', -1, 64))
	if showBillable {
		cell(strconv.FormatFloat(round2(float64(totalCost)/100), 'f', -1, 64))
	}
	b.WriteString(`</table:table-row></table:table>`)
	return odsPackage(b.String())
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// odsPackage wraps a table fragment in the minimal ODS file set.
func odsPackage(tableXML string) ([]byte, error) {
	content := `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" office:version="1.2"><office:body><office:spreadsheet>` + tableXML + `</office:spreadsheet></office:body></office:document-content>`
	manifest := `<?xml version="1.0" encoding="UTF-8"?>
<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.2"><manifest:file-entry manifest:full-path="/" manifest:media-type="application/vnd.oasis.opendocument.spreadsheet"/><manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/></manifest:manifest>`
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	// mimetype must be first and stored uncompressed.
	mh := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	w, err := zw.CreateHeader(mh)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte("application/vnd.oasis.opendocument.spreadsheet")); err != nil {
		return nil, err
	}
	for name, body := range map[string]string{
		"content.xml":           content,
		"META-INF/manifest.xml": manifest,
	} {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeDetailedODS is the same table for the detailed (per-entry) export.
func writeDetailedODS(rows []detailedRow, l localization) ([]byte, error) {
	var b strings.Builder
	cell := func(v string) {
		b.WriteString(`<table:table-cell office:value-type="string"><text:p>`)
		b.WriteString(template.HTMLEscapeString(v))
		b.WriteString(`</text:p></table:table-cell>`)
	}
	b.WriteString(`<table:table table:name="Sheet1"><table:table-row>`)
	for _, h := range detailedHeader {
		cell(h)
	}
	b.WriteString(`</table:table-row>`)
	for _, r := range rows {
		b.WriteString(`<table:table-row>`)
		for i, c := range r.csvCells(l, l.formatInterval) {
			if i == 8 && r.hasEnd {
				// Upstream ODS columnFormats: Duration (decimal) numeric 0.00.
				b.WriteString(`<table:table-cell office:value-type="float" office:value="`)
				b.WriteString(c)
				b.WriteString(`"><text:p>`)
				b.WriteString(c)
				b.WriteString(`</text:p></table:table-cell>`)
				continue
			}
			cell(c)
		}
		b.WriteString(`</table:table-row>`)
	}
	b.WriteString(`</table:table>`)
	return odsPackage(b.String())
}

// ---- Gotenberg ----

// gotenbergPDF posts multipart to /forms/chromium/convert/html, mirroring
// gotenberg-php: index.html + footer.html + assets + margin/paper fields.
func (s *Server) gotenbergPDF(indexHTML, footerHTML string, assets map[string][]byte, waitExpr string) ([]byte, error) {
	if s.gotenbergURL == "" {
		return nil, fmt.Errorf("pdf renderer is not configured")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	addFile := func(name, filename string, content []byte) error {
		part, err := mw.CreateFormFile("files", filename)
		if err != nil {
			return err
		}
		_, err = part.Write(content)
		return err
	}
	if err := addFile("files", "index.html", []byte(indexHTML)); err != nil {
		return nil, err
	}
	if footerHTML != "" {
		if err := addFile("files", "footer.html", []byte(footerHTML)); err != nil {
			return nil, err
		}
	}
	for name, content := range assets {
		if err := addFile("files", name, content); err != nil {
			return nil, err
		}
	}
	fields := map[string]string{
		"marginTop":    "0.39",
		"marginBottom": "0.78",
		"marginLeft":   "0.39",
		"marginRight":  "0.39",
		"paperWidth":   "8.27",
		"paperHeight":  "11.7",
	}
	if waitExpr != "" {
		fields["waitForExpression"] = waitExpr
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(s.gotenbergURL, "/")+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("gotenberg: %s: %s", resp.Status, strings.TrimSpace(string(slurp)))
	}
	return io.ReadAll(resp.Body)
}

// ---- PDF templates (ports of the upstream blade views) ----

var pdfFooterTemplate = template.Must(template.New("footer").Parse(`<!DOCTYPE html>
<html lang="en"><head><style>
.page-number{position:absolute;bottom:0;left:0;font-size:12px;margin-left:46px;margin-bottom:40px}
</style></head><body>
<div class="page-number">Page <span class="pageNumber"></span> of <span class="totalPages"></span></div>
</body></html>`))

const pdfCommonCSS = `html, body, div, span, applet, object, iframe,
h1, h2, h3, h4, h5, h6, p, blockquote, pre,
a, abbr, acronym, address, big, cite, code,
del, dfn, em, img, ins, kbd, q, s, samp,
small, strike, strong, sub, sup, tt, var,
b, u, i, center,
dl, dt, dd, ol, ul, li,
fieldset, form, label, legend,
table, caption, tbody, tfoot, thead, tr, th, td,
article, aside, canvas, details, embed,
figure, figcaption, footer, header, hgroup,
menu, nav, output, ruby, section, summary,
time, mark, audio, video {
    margin: 0; padding: 0; border: 0; font-size: 100%;
    vertical-align: baseline; box-sizing: border-box;
}
article, aside, details, figcaption, figure,
footer, header, hgroup, menu, nav, section { display: block; }
body { line-height: 1; }
ol, ul { list-style: none; }
blockquote, q { quotes: none; }
blockquote:before, blockquote:after,
q:before, q:after { content: ''; content: none; }
@font-face { font-family: 'Outfit'; src: url('outfit.ttf'); }
body { font-family: 'Outfit', 'Helvetica Neue', 'Helvetica', Helvetica, Arial, sans-serif; color: #18181b }
table { font-size: 10px; }
table thead { background-color: #eee; }
.table-wrapper table th { background-color: #fafafa; }
.table-wrapper { border: 1px solid #d4d4d8; border-radius: 8px; overflow: hidden; width: calc(100% - 2px) }
table { border-collapse: collapse; border-spacing: 0; text-align: left; }
thead { border-bottom: 1px #d4d4d8 solid; }
tfoot { border-top: 1px #d4d4d8 solid; }
table th, table tfoot td { font-weight: 500; padding: 6px 12px; color: #18181b; }
table td, table th { font-size: 12px; }
table tr { border-bottom: 1px #e4e4e7 solid; }
table tr:last-of-type { border-bottom: none; }
table tr td { font-weight: 400; color: #3f3f46; padding: 6px 12px; }`

// detailedPDFData feeds the time-entry-index template.
type detailedPDFData struct {
	Loc           *localization
	Timezone      string
	StartLabel    string
	EndLabel      string
	ShowBillable  bool
	TotalDuration string
	TotalCost     string
	Entries       []pdfEntry
}

// pdfEntry is detailedRow with exported fields — html/template cannot read
// unexported struct fields.
type pdfEntry struct {
	Description, Task, Project, Client, User string
	Start, End                               time.Time
	HasEnd                                   bool
	Duration                                 int64
	Billable, IsBreak                        bool
	Tags                                     string
}

func pdfEntries(rows []detailedRow) []pdfEntry {
	out := make([]pdfEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, pdfEntry{
			Description: r.description, Task: r.task, Project: r.project,
			Client: r.client, User: r.user, Start: r.start, End: r.end,
			HasEnd: r.hasEnd, Duration: r.duration, Billable: r.billable,
			IsBreak: r.isBreak, Tags: r.tags,
		})
	}
	return out
}

var detailedPDFTemplate = template.Must(template.New("detailed").Funcs(template.FuncMap{
	"fmtTime": func(l *localization, t time.Time) string { return l.formatTime(t) },
	"fmtDate": func(l *localization, t time.Time) string { return l.formatDate(t) },
	"fmtDur":  func(l *localization, s int64) string { return l.formatIntervalForReporting(s) },
	"sameDay": func(a, b time.Time) bool { return a.Format("2006-01-02") == b.Format("2006-01-02") },
	"or": func(a string, b string) string {
		if a == "" {
			return b
		}
		return a
	},
}).Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8" /><title>Report</title>
<style>` + pdfCommonCSS + `</style></head>
<body>
<div>
    <p style="font-size: 32px; font-weight: 600; margin-bottom: 5px;">Detailed Report</p>
    <div style="font-size: 16px; font-weight: 600; color: #71717a;">
        <span>{{ .StartLabel }} - {{ .EndLabel }}</span><br><br>
    </div>
</div>
<div class="table-wrapper">
    <div style="background-color: #fafafa; padding: 5px 14px; display: flex; gap: 20px;">
        <div style="padding: 8px 12px; border-radius: 8px;">
            <div style="color: #71717a; font-weight: 600;">Duration</div>
            <div style="font-size: 24px; font-weight: 500; margin-top: 2px;">{{ .TotalDuration }} </div>
        </div>
        {{ if .ShowBillable }}
        <div style="padding: 8px 12px; border-radius: 8px;">
            <div style="color: #71717a; font-weight: 600;">Total cost</div>
            <div style="font-size: 24px; font-weight: 500; margin-top: 2px;">{{ .TotalCost }}</div>
        </div>
        {{ end }}
    </div>
    <div>
        <table style="width: 100%;">
            <thead>
            <tr style="border-top: 1px #d4d4d8 solid;">
                <th>Time Entry</th>
                <th>User</th>
                <th style="text-align: center;">Time</th>
                <th>Duration</th>
                <th>Billable</th>
                <th>Break</th>
                <th>Tags</th>
            </tr>
            </thead>
            <tbody>
            {{ range .Entries }}
                <tr>
                    <td style="overflow-wrap: break-word; max-width: 250px;">
                        {{ or .Description "-" }} <br>
                        {{ if .Task }}<span style="font-weight: 600;">Task:</span> {{ .Task }} <br>{{ end }}
                        {{ if .Project }}<span style="font-weight: 600;">Project:</span> {{ .Project }} <br>{{ end }}
                        {{ if .Client }}<span style="font-weight: 600;">Client:</span>{{ .Client }} <br>{{ end }}
                    </td>
                    <td style="overflow-wrap: break-word; min-width: 75px;">{{ .User }}</td>
                    <td style="overflow-wrap: break-word; min-width: 150px; text-align: center;">
                        {{ if and .HasEnd (not (sameDay .Start .End)) }}
                            {{ fmtDate $.Loc .Start }} - <br> {{ fmtDate $.Loc .End }}
                        {{ else }}
                            {{ fmtDate $.Loc .Start }}
                        {{ end }}
                        <br>
                        {{ fmtTime $.Loc .Start }} - {{ if .HasEnd }}{{ fmtTime $.Loc .End }}{{ end }}
                    </td>
                    <td style="overflow-wrap: break-word; min-width: 75px;">
                        {{ if .HasEnd }}{{ fmtDur $.Loc .Duration }}{{ end }}
                    </td>
                    <td style="overflow-wrap: break-word;">{{ if .Billable }}Yes{{ else }}No{{ end }}</td>
                    <td style="overflow-wrap: break-word;">{{ if .IsBreak }}Yes{{ else }}No{{ end }}</td>
                    <td style="overflow-wrap: break-word; min-width: 75px;">{{ or .Tags "-" }}</td>
                </tr>
            {{ end }}
            </tbody>
        </table>
    </div>
</div>
</body></html>`))

// ---- aggregate PDF ----

type aggPDFGroup1 struct {
	Label    string
	Color    string
	Duration string
	Cost     string
	Rows     []aggPDFGroup2
}
type aggPDFGroup2 struct {
	Label    string
	Duration string
	Hours    string
	Cost     string
}

type aggPDFData struct {
	Loc           *localization
	StartLabel    string
	EndLabel      string
	ShowBillable  bool
	TotalDuration string
	TotalCost     string
	GroupLabel    string
	SubGroupLabel string
	Groups        []aggPDFGroup1
	PieData       template.JS
	ChartLabels   template.JS
	ChartValues   template.JS
	ChartRotate   bool
	Debug         bool
}

var aggregatePDFTemplate = template.Must(template.New("aggregate").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8" /><title>Report</title>
<style>` + pdfCommonCSS + `
.data-table { break-after: auto; }
.no-break { break-after: avoid-page; }
</style>
<script>window.status = "processing";</script>
<script src="{{ if .Debug }}https://cdn.jsdelivr.net/npm/echarts@5.5.1/dist/echarts.min.js{{ else }}echarts.min.js{{ end }}"></script>
{{ if .Debug }}<link rel="preconnect" href="https://fonts.bunny.net">
<link href="https://fonts.bunny.net/css?family=outfit:200,300,400,500,600,700,800" rel="stylesheet"/>{{ end }}
</head>
<body>
<div>
    <p style="font-size: 32px; font-weight: 600; margin-bottom: 5px;">Report</p>
    <div style="font-size: 16px; font-weight: 600; color: #71717a;">
        <span>{{ .StartLabel }} - {{ .EndLabel }}</span><br><br>
    </div>
</div>
<div class="table-wrapper">
    <div style="background-color: #fafafa; padding: 5px 14px; border-bottom: 1px #d4d4d8 solid; display: flex; gap: 20px;">
        <div style="padding: 8px 12px; border-radius: 8px;">
            <div style="color: #71717a; font-weight: 600;">Duration</div>
            <div style="font-size: 24px; font-weight: 500; margin-top: 2px;">{{ .TotalDuration }} </div>
        </div>
        {{ if .ShowBillable }}
        <div style="padding: 8px 12px; border-radius: 8px;">
            <div style="color: #71717a; font-weight: 600;">Total cost</div>
            <div style="font-size: 24px; font-weight: 500; margin-top: 2px;">{{ .TotalCost }} </div>
        </div>
        {{ end }}
    </div>
    <div id="main-chart" style="width: 700px; height: 300px; margin: 20px auto;"></div>
</div>
<div style="display: flex; align-items: center; padding-top: 40px;">
    <div style="padding: 10px 0;">
        <div id="pie-chart" style="width: 300px; height: 180px; margin-bottom: 20px;"></div>
    </div>
    <div style="flex: 1 1 0%;">
        <div class="">
            <table style="width: 100%; ">
                <thead>
                <tr>
                    <th>{{ .GroupLabel }}</th>
                    <th>Duration</th>
                    {{ if .ShowBillable }}<th style="text-align: right;">Cost</th>{{ end }}
                </tr>
                </thead>
                {{ range .Groups }}
                    <tr>
                        <td style="display: flex; align-items: center;">
                            <div style="width: 12px; height: 12px; border-radius: 50%; background-color: {{ .Color }};"></div>
                            <span style="padding-left: 8px;">{{ .Label }}</span>
                        </td>
                        <td style="text-align: left;">{{ .Duration }}</td>
                        {{ if $.ShowBillable }}<td style="text-align: right;">{{ .Cost }}</td>{{ end }}
                    </tr>
                {{ end }}
                <tfoot>
                <tr>
                    <td style="font-weight: 500;color: #18181b;">Total</td>
                    <td style="font-weight: 500;color: #18181b;">{{ .TotalDuration }}</td>
                    {{ if .ShowBillable }}<td style="text-align: right; font-weight: 500;color: #18181b;">{{ .TotalCost }}</td>{{ end }}
                </tr>
                </tfoot>
            </table>
        </div>
    </div>
</div>
{{ range .Groups }}
    <div class="data-table">
        <h2 class="no-break" style="padding-top: 16px; padding-bottom: 8px; font-size: 16px; font-weight: 600; padding-left: 6px; color: #3f3f46;">
            <span style="color: #a1a1aa;">{{ $.GroupLabel }}:</span>
            {{ .Label }}
        </h2>
        <div class="table-wrapper">
            <table style="width: 100%;">
                <thead>
                <tr>
                    <th>{{ $.SubGroupLabel }}</th>
                    <th>Duration</th>
                    <th>Duration (h)</th>
                    {{ if $.ShowBillable }}<th>Cost</th>{{ end }}
                </tr>
                </thead>
                <tbody>
                {{ range .Rows }}
                    <tr>
                        <td>{{ .Label }}</td>
                        <td>{{ .Duration }}</td>
                        <td>{{ .Hours }}</td>
                        {{ if $.ShowBillable }}<td>{{ .Cost }}</td>{{ end }}
                    </tr>
                {{ end }}
                </tbody>
            </table>
        </div>
    </div>
{{ end }}
<script>
    let elementPieChart = document.getElementById("pie-chart");
    let pieChart = echarts.init(elementPieChart, null, { renderer: "svg" });
    let pieChartOptions = {
        animation: false,
        backgroundColor: "transparent",
        series: [{
            data: {{ .PieData }},
            radius: ["40%", "80%"],
            type: "pie",
            label: { formatter: "{d}%", overflow: "truncate" }
        }]
    };
    pieChart.on("finished", () => {
        window.pieChartFinished = true;
        if (window.mainChartFinished && window.pieChartFinished) { window.status = "ready"; }
    });
    pieChart.setOption(pieChartOptions);

    let elementMainChart = document.getElementById("main-chart");
    let mainChart = echarts.init(elementMainChart, null, { renderer: "svg" });
    let mainChartOptions = {
        animation: false,
        tooltip: {},
        xAxis: {
            data: {{ .ChartLabels }},
            axisLabel: { fontSize: 10, fontWeight: 400, color: "rgb(120, 120, 120)", margin: 16, fontFamily: "Outfit, sans-serif" },
            axisTick: { interval: 0, alignWithLabel: true }
        },
        grid: { containLabel: true, left: 15, top: 15, right: 15, bottom: 0 },
        yAxis: { minInterval: 1, axisLabel: { show: false, inside: true } },
        series: [{
            name: "time",
            type: "bar",
            data: {{ .ChartValues }},
            itemStyle: { borderColor: "#7dd3fc", color: "#7dd3fc" },
            label: {
                show: true,
                {{ if .ChartRotate }}rotate: 90, offset: [10, 5],{{ end }}
                fontSize: 10,
                position: "top",
                formatter: function (params) { return params.name; }
            }
        }]
    };
    mainChart.on("finished", () => {
        window.mainChartFinished = true;
        if (window.mainChartFinished && window.pieChartFinished) { window.status = "ready"; }
    });
    mainChart.setOption(mainChartOptions);
</script>
</body></html>`))

// saveExportFile writes bytes to storageDir/exports and returns the signed URL.
func (s *Server) saveExportFile(filename string, content []byte) (string, error) {
	dir := filepath.Join(s.storageDir, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	expires := time.Now().Add(5 * time.Minute).Unix()
	sig := exportSignature(s.appKey, filename, expires)
	return fmt.Sprintf("%s/exports/%s?expires=%d&signature=%s", s.appURL, filename, expires, sig), nil
}
