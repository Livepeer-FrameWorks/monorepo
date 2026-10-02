package grpc

import (
	"bytes"
	"compress/zlib"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

// pdfTextRuns returns the strings a PDF's page content streams draw with Tj,
// in drawing order. Billing documents write text in an embedded TrueType font
// addressed by Unicode code point, two bytes per character.
func pdfTextRuns(t *testing.T, document []byte) []string {
	t.Helper()
	if !bytes.HasPrefix(document, []byte("%PDF-")) {
		t.Fatalf("document is not a PDF: %.40q", document)
	}
	var runs []string
	for _, object := range bytes.Split(document, []byte("endobj")) {
		start := bytes.Index(object, []byte("stream\n"))
		end := bytes.LastIndex(object, []byte("endstream"))
		if start < 0 || end < start {
			continue
		}
		dictionary := object[:start]
		if bytes.Contains(dictionary, []byte("/Length1")) || bytes.Contains(dictionary, []byte("/Subtype")) {
			continue // embedded font program or image
		}
		data := bytes.TrimRight(object[start+len("stream\n"):end], "\r\n")
		if bytes.Contains(dictionary, []byte("/FlateDecode")) {
			reader, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				continue
			}
			inflated, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("inflate content stream: %v", err)
			}
			data = inflated
		}
		if !bytes.Contains(data, []byte("Tj")) {
			continue
		}
		runs = append(runs, contentStreamStrings(data)...)
	}
	return runs
}

// contentStreamStrings decodes the literal strings shown with Tj.
func contentStreamStrings(content []byte) []string {
	var runs []string
	for i := 0; i < len(content); i++ {
		if content[i] != '(' {
			continue
		}
		var raw []byte
		depth := 1
		j := i + 1
		for ; j < len(content) && depth > 0; j++ {
			c := content[j]
			switch {
			case c == '\\' && j+1 < len(content):
				j++
				switch next := content[j]; next {
				case 'n':
					raw = append(raw, '\n')
				case 'r':
					raw = append(raw, '\r')
				case 't':
					raw = append(raw, '\t')
				case 'b':
					raw = append(raw, '\b')
				case 'f':
					raw = append(raw, '\f')
				default:
					if next >= '0' && next <= '7' {
						value, digits := 0, 0
						for digits < 3 && j < len(content) && content[j] >= '0' && content[j] <= '7' {
							value = value*8 + int(content[j]-'0')
							j++
							digits++
						}
						j--
						raw = append(raw, byte(value))
					} else {
						raw = append(raw, next)
					}
				}
			case c == '(':
				depth++
				raw = append(raw, c)
			case c == ')':
				depth--
				if depth > 0 {
					raw = append(raw, c)
				}
			default:
				raw = append(raw, c)
			}
		}
		rest := bytes.TrimLeft(content[j:], " \r\n\t")
		if bytes.HasPrefix(rest, []byte("Tj")) {
			units := make([]uint16, 0, len(raw)/2)
			for k := 0; k+1 < len(raw); k += 2 {
				units = append(units, uint16(raw[k])<<8|uint16(raw[k+1]))
			}
			runs = append(runs, string(utf16.Decode(units)))
		}
		i = j - 1
	}
	return runs
}

// requireRuns fails unless every wanted text appears within one drawn run.
func requireRuns(t *testing.T, runs []string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		found := false
		for _, run := range runs {
			if strings.Contains(run, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PDF text lacks %q; drawn runs:\n%s", want, strings.Join(runs, "\n"))
		}
	}
}
