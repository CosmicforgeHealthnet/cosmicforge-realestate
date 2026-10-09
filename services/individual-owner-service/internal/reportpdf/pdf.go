// Package reportpdf renders owner reports and privacy exports as simple,
// text-only PDF documents (Helvetica, A4, paginated). media-document-service
// stores documents as PDF; this renderer has no dependencies and produces
// deterministic output for the same input.
package reportpdf

import (
	"bytes"
	"fmt"
	"strings"
)

// Section is a heading with its lines.
type Section struct {
	Heading string
	Lines   []string
}

const (
	pageWidth    = 595
	pageHeight   = 842
	margin       = 50
	lineHeight   = 14
	maxLineChars = 95
)

type line struct {
	text string
	bold bool
}

// Render produces a PDF with a title, a subtitle and sections.
func Render(title, subtitle string, sections []Section) []byte {
	var lines []line
	lines = append(lines, line{text: title, bold: true}, line{text: subtitle}, line{})
	for _, s := range sections {
		lines = append(lines, line{text: s.Heading, bold: true})
		for _, l := range s.Lines {
			for _, w := range wrap(l) {
				lines = append(lines, line{text: w})
			}
		}
		lines = append(lines, line{})
	}
	perPage := (pageHeight - 2*margin) / lineHeight
	var pages [][]line
	for len(lines) > 0 {
		n := min(perPage, len(lines))
		pages = append(pages, lines[:n])
		lines = lines[n:]
	}
	if len(pages) == 0 {
		pages = [][]line{{}}
	}

	// Objects: 1 catalog, 2 pages, 3 font, 4 bold font, then per page a
	// page object and a content stream.
	var objs []string
	objs = append(objs, "<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>")
	var kids []string
	for i, p := range pages {
		pageObj := 5 + 2*i
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj))
		var c strings.Builder
		y := pageHeight - margin
		for _, l := range p {
			font := "F1"
			if l.bold {
				font = "F2"
			}
			if l.text != "" {
				fmt.Fprintf(&c, "BT /%s 10 Tf %d %d Td (%s) Tj ET\n", font, margin, y, escape(l.text))
			}
			y -= lineHeight
		}
		fmt.Fprintf(&c, "BT /F1 8 Tf %d %d Td (Page %d of %d) Tj ET\n", margin, margin/2, i+1, len(pages))
		stream := c.String()
		objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			pageWidth, pageHeight, pageObj+1),
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// escape keeps printable ASCII and escapes PDF string delimiters.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 32 && r < 127:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func wrap(s string) []string {
	if len(s) <= maxLineChars {
		return []string{s}
	}
	var out []string
	words := strings.Fields(s)
	cur := ""
	for _, w := range words {
		for len(w) > maxLineChars {
			if cur != "" {
				out, cur = append(out, cur), ""
			}
			out, w = append(out, w[:maxLineChars]), w[maxLineChars:]
		}
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= maxLineChars:
			cur += " " + w
		default:
			out, cur = append(out, cur), w
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
