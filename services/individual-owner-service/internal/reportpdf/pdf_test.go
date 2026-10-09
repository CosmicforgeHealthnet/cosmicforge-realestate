package reportpdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRenderStructure(t *testing.T) {
	var lines []string
	for i := range 120 {
		lines = append(lines, fmt.Sprintf("Line %d (with parens) and a \\ backslash and naïve text", i))
	}
	lines = append(lines, strings.Repeat("word ", 60), strings.Repeat("x", 250))
	pdf := Render("Portfolio report", "2027-01-01 to 2027-03-31", []Section{{Heading: "Income", Lines: lines}})
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4\n")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatal("not a PDF")
	}
	if !bytes.Contains(pdf, []byte(`\(with parens\)`)) || !bytes.Contains(pdf, []byte(`na?ve`)) {
		t.Fatal("escaping")
	}
	if !bytes.Contains(pdf, []byte("/Count 3")) {
		t.Fatalf("expected 3 pages")
	}
	// Every xref offset points at its object.
	start := bytes.LastIndex(pdf, []byte("startxref\n"))
	var xref int
	fmt.Sscanf(string(pdf[start+10:]), "%d", &xref)
	entries := strings.Split(string(pdf[xref:]), "\n")[3:]
	for i, e := range entries {
		if !strings.HasSuffix(e, " n ") {
			break
		}
		var off int
		fmt.Sscanf(e, "%d", &off)
		if want := fmt.Sprintf("%d 0 obj", i+1); !bytes.HasPrefix(pdf[off:], []byte(want)) {
			t.Fatalf("xref %d -> %q", i+1, pdf[off:off+10])
		}
	}
	if !bytes.Equal(pdf, Render("Portfolio report", "2027-01-01 to 2027-03-31", []Section{{Heading: "Income", Lines: lines}})) {
		t.Fatal("not deterministic")
	}
}
