package grpc

import (
	"bytes"
	"fmt"
	"strings"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
)

// Billing documents are PDFs drawn with the Go fonts, embedded as subsets,
// which cover Latin, Greek and Cyrillic text and the € sign. The same data
// renders to the same bytes: the PDF dates are the document's issue time and
// the catalog is written in sorted order.
var (
	billingDocumentRegularFont = goregular.TTF
	billingDocumentBoldFont    = gobold.TTF
)

const (
	billingDocumentFont   = "go"
	billingDocumentMargin = 18.0
	// billingDocumentBottom is the space kept free for the footer.
	billingDocumentBottom = 22.0
	billingDocumentWidth  = 210.0 - 2*billingDocumentMargin
)

// Line table columns in millimetres; they span billingDocumentWidth.
var billingDocumentColumns = [4]float64{92, 30, 26, 26}

type billingDocumentPDF struct {
	*fpdf.Fpdf
	data billingDocumentData
}

func (p *billingDocumentPDF) text(style string, size float64, gray int) {
	p.SetFont(billingDocumentFont, style, size)
	p.SetTextColor(gray, gray, gray)
}

// fits starts a new page unless height millimetres fit above the footer.
func (p *billingDocumentPDF) fits(height float64) bool {
	_, pageHeight := p.GetPageSize()
	if p.GetY()+height <= pageHeight-billingDocumentBottom {
		return true
	}
	p.AddPage()
	return false
}

// renderBillingDocumentPDF draws one billing document: the header with its
// number, issue time and amount, the supplier and the customer, the document's
// fields, its line items and totals, and a footer with the retention notice.
func renderBillingDocumentPDF(data billingDocumentData) ([]byte, error) {
	doc := fpdf.New("P", "mm", "A4", "")
	pdf := &billingDocumentPDF{Fpdf: doc, data: data}
	pdf.SetCatalogSort(true)
	pdf.SetCreationDate(data.IssuedTime)
	pdf.SetModificationDate(data.IssuedTime)
	pdf.SetTitle(strings.TrimSpace(data.Title+" "+data.Number), true)
	pdf.SetAuthor(data.SupplierName, true)
	pdf.AddUTF8FontFromBytes(billingDocumentFont, "", billingDocumentRegularFont)
	pdf.AddUTF8FontFromBytes(billingDocumentFont, "B", billingDocumentBoldFont)
	pdf.SetMargins(billingDocumentMargin, billingDocumentMargin, billingDocumentMargin)
	pdf.SetAutoPageBreak(true, billingDocumentBottom)
	pdf.AliasNbPages("{nb}")
	pdf.SetFooterFunc(pdf.footer)
	pdf.AddPage()

	pdf.header()
	pdf.parties()
	pdf.fields()
	if len(data.Lines) > 0 {
		pdf.lines()
	}
	if len(data.Totals) > 0 {
		pdf.totals()
	}
	if err := pdf.Error(); err != nil {
		return nil, fmt.Errorf("draw billing document: %w", err)
	}
	var content bytes.Buffer
	if err := pdf.Output(&content); err != nil {
		return nil, fmt.Errorf("write billing document: %w", err)
	}
	return content.Bytes(), nil
}

func (p *billingDocumentPDF) header() {
	top := p.GetY()
	left := billingDocumentWidth * 0.6
	p.text("B", 20, 24)
	p.CellFormat(left, 9, p.data.Title, "", 2, "L", false, 0, "")
	p.text("", 10, 24)
	p.CellFormat(left, 5.5, p.data.Number, "", 2, "L", false, 0, "")
	p.text("", 9, 83)
	p.CellFormat(left, 5, "Issued "+p.data.IssuedAt, "", 2, "L", false, 0, "")
	bottom := p.GetY()

	right := billingDocumentWidth - left
	p.SetXY(billingDocumentMargin+left, top)
	p.text("B", 15, 24)
	p.CellFormat(right, 9, strings.TrimSpace(p.data.Currency+" "+p.data.Amount), "", 2, "R", false, 0, "")
	p.SetX(billingDocumentMargin + left)
	p.text("", 9, 83)
	p.CellFormat(right, 5.5, p.data.Status, "", 2, "R", false, 0, "")

	p.SetY(bottom + 3)
	p.SetDrawColor(24, 32, 42)
	p.SetLineWidth(0.5)
	p.Line(billingDocumentMargin, p.GetY(), billingDocumentMargin+billingDocumentWidth, p.GetY())
	p.Ln(6)
}

// party draws one party block in a column and returns the y below it.
func (p *billingDocumentPDF) party(x, width, top float64, heading string, emphasis string, lines []string) float64 {
	p.SetXY(x, top)
	p.text("B", 8, 83)
	p.CellFormat(width, 5, heading, "", 2, "L", false, 0, "")
	if emphasis != "" {
		p.SetX(x)
		p.text("B", 10, 24)
		p.MultiCell(width, 5, emphasis, "", "L", false)
	}
	p.text("", 9, 24)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p.SetX(x)
		p.MultiCell(width, 4.6, line, "", "L", false)
	}
	return p.GetY()
}

func (p *billingDocumentPDF) parties() {
	d := p.data
	top := p.GetY()
	column := (billingDocumentWidth - 8) / 2
	supplier := []string{d.SupplierAddress}
	if d.SupplierVAT != "" {
		supplier = append(supplier, "VAT "+d.SupplierVAT)
	}
	if d.SupplierRegistration != "" {
		supplier = append(supplier, "Registration "+d.SupplierRegistration)
	}
	supplierBottom := p.party(billingDocumentMargin, column, top, "Supplier", d.SupplierName, supplier)

	name := d.Customer
	customer := []string{}
	if name == "" {
		name = d.CustomerCompany
	} else {
		customer = append(customer, d.CustomerCompany)
	}
	customer = append(customer, d.CustomerAddress...)
	if d.CustomerVAT != "" {
		customer = append(customer, "VAT "+d.CustomerVAT)
	}
	if d.CustomerEmail != "" {
		customer = append(customer, d.CustomerEmail)
	}
	if name == "" && len(d.CustomerAddress) == 0 {
		customer = append(customer, "No billing name or address on file")
	}
	customerBottom := p.party(billingDocumentMargin+column+8, column, top, "Customer", name, customer)
	p.SetY(max(supplierBottom, customerBottom) + 6)
}

func (p *billingDocumentPDF) fields() {
	const labelWidth = 74.0
	for _, field := range p.data.Fields {
		p.text("", 9, 24)
		valueLines := p.SplitText(field.Value, billingDocumentWidth-labelWidth)
		p.fits(4.8 * float64(max(len(valueLines), 1)))
		top := p.GetY()
		p.text("B", 9, 24)
		p.SetX(billingDocumentMargin)
		p.MultiCell(labelWidth-2, 4.8, field.Label, "", "L", false)
		labelBottom := p.GetY()
		p.SetXY(billingDocumentMargin+labelWidth, top)
		p.text("", 9, 24)
		p.MultiCell(billingDocumentWidth-labelWidth, 4.8, field.Value, "", "L", false)
		p.SetY(max(labelBottom, p.GetY()) + 0.8)
	}
	p.Ln(4)
}

func (p *billingDocumentPDF) lineHeader() {
	p.SetFillColor(236, 239, 242)
	p.text("B", 8.5, 24)
	p.SetX(billingDocumentMargin)
	amount := "Amount"
	if p.data.LineCurrency != "" {
		amount = "Amount (" + p.data.LineCurrency + ")"
	}
	for i, heading := range []string{"Description", "Quantity", "Unit price", amount} {
		align := "R"
		if i == 0 {
			align = "L"
		}
		p.CellFormat(billingDocumentColumns[i], 6.5, heading, "", 0, align, true, 0, "")
	}
	p.Ln(6.5)
}

func (p *billingDocumentPDF) lines() {
	p.fits(6.5 + 9)
	p.lineHeader()
	descriptionWidth := billingDocumentColumns[0] - 2
	for _, line := range p.data.Lines {
		p.text("", 9, 24)
		description := p.SplitText(line.Description, descriptionWidth)
		p.text("", 7.5, 83)
		var detail []string
		if line.Detail != "" {
			detail = p.SplitText(line.Detail, descriptionWidth)
		}
		height := 4.6*float64(len(description)) + 3.8*float64(len(detail)) + 2.4
		if !p.fits(height) {
			p.lineHeader()
		}
		top := p.GetY() + 1.2
		x := billingDocumentMargin
		p.SetXY(x, top)
		p.text("", 9, 24)
		for _, text := range description {
			p.SetX(x)
			p.CellFormat(billingDocumentColumns[0], 4.6, text, "", 2, "L", false, 0, "")
		}
		p.text("", 7.5, 83)
		for _, text := range detail {
			p.SetX(x)
			p.CellFormat(billingDocumentColumns[0], 3.8, text, "", 2, "L", false, 0, "")
		}
		p.SetXY(x+billingDocumentColumns[0], top)
		p.text("", 9, 24)
		for i, value := range []string{line.Quantity, line.UnitPrice, line.Amount} {
			p.CellFormat(billingDocumentColumns[i+1], 4.6, value, "", 0, "R", false, 0, "")
		}
		bottom := top + height - 1.2
		p.SetDrawColor(204, 211, 218)
		p.SetLineWidth(0.2)
		p.Line(billingDocumentMargin, bottom, billingDocumentMargin+billingDocumentWidth, bottom)
		p.SetY(bottom)
	}
	p.Ln(4)
}

func (p *billingDocumentPDF) totals() {
	const labelWidth, valueWidth = 62.0, 34.0
	x := billingDocumentMargin + billingDocumentWidth - labelWidth - valueWidth
	p.fits(5.5 * float64(len(p.data.Totals)))
	for i, total := range p.data.Totals {
		style := ""
		if i == len(p.data.Totals)-1 {
			style = "B"
		}
		p.text(style, 9.5, 24)
		p.SetX(x)
		p.CellFormat(labelWidth, 5.5, total.Label, "", 0, "L", false, 0, "")
		p.CellFormat(valueWidth, 5.5, total.Value, "", 1, "R", false, 0, "")
	}
}

func (p *billingDocumentPDF) footer() {
	p.SetY(-16)
	p.SetDrawColor(204, 211, 218)
	p.SetLineWidth(0.2)
	p.Line(billingDocumentMargin, p.GetY(), billingDocumentMargin+billingDocumentWidth, p.GetY())
	p.Ln(1.5)
	p.text("", 7.5, 83)
	const pageWidth = 26.0
	p.SetX(billingDocumentMargin)
	p.CellFormat(billingDocumentWidth-pageWidth, 3.8, "Retained until at least "+p.data.RetentionUntil+".", "", 0, "L", false, 0, "")
	p.CellFormat(pageWidth, 3.8, fmt.Sprintf("Page %d of {nb}", p.PageNo()), "", 1, "R", false, 0, "")
	p.SetX(billingDocumentMargin)
	p.CellFormat(billingDocumentWidth, 3.8, "Document number and settlement references are immutable audit identifiers.", "", 0, "L", false, 0, "")
}
