package grpc

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strconv"
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
	for _, stream := range pdfStreams(t, document) {
		if bytes.Contains(stream.dictionary, []byte("/Length1")) || bytes.Contains(stream.dictionary, []byte("/Subtype")) {
			continue // embedded font program or image
		}
		data := stream.data
		if bytes.Contains(stream.dictionary, []byte("/FlateDecode")) {
			reader, err := zlib.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("open content stream: %v", err)
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

type pdfStream struct {
	dictionary []byte
	data       []byte
}

var pdfStreamLength = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)

// pdfStreams returns every stream object's dictionary and its raw bytes. A
// stream holds exactly /Length bytes after the EOL that ends the "stream"
// keyword (PDF 32000-1 7.3.8.1). Compressed data can end in CR or LF bytes and
// can contain "endstream" or "endobj", so the bytes are counted, never
// delimited by keywords, and the parser skips over them before searching for
// the next stream.
func pdfStreams(t *testing.T, document []byte) []pdfStream {
	t.Helper()
	var streams []pdfStream
	position := 0
	for {
		offset := bytes.Index(document[position:], []byte("stream"))
		if offset < 0 {
			return streams
		}
		keyword := position + offset
		if !bytes.HasSuffix(bytes.TrimRight(document[position:keyword], " \r\n\t"), []byte(">>")) {
			position = keyword + len("stream")
			continue // the word inside a string or name, not a keyword after a stream dictionary
		}
		objectStart := bytes.LastIndex(document[position:keyword], []byte(" obj"))
		if objectStart < 0 {
			t.Fatalf("PDF stream at byte %d is outside an object", keyword)
		}
		dictionary := document[position+objectStart : keyword]
		match := pdfStreamLength.FindSubmatch(dictionary)
		if match == nil || len(match[2]) > 0 {
			t.Fatalf("PDF stream at byte %d has no direct /Length: %q", keyword, dictionary)
		}
		length, err := strconv.Atoi(string(match[1]))
		if err != nil {
			t.Fatalf("PDF stream /Length %q: %v", match[1], err)
		}
		start := keyword + len("stream")
		switch {
		case bytes.HasPrefix(document[start:], []byte("\r\n")):
			start += 2
		case bytes.HasPrefix(document[start:], []byte("\n")):
			start++
		default:
			t.Fatalf("PDF stream keyword at byte %d is not followed by an EOL", keyword)
		}
		end := start + length
		if end > len(document) {
			t.Fatalf("PDF stream at byte %d declares /Length %d past the end of the document", keyword, length)
		}
		if !bytes.HasPrefix(bytes.TrimLeft(document[end:], "\r\n"), []byte("endstream")) {
			t.Fatalf("PDF stream at byte %d is not followed by endstream after its /Length %d", keyword, length)
		}
		streams = append(streams, pdfStream{dictionary: dictionary, data: document[start:end]})
		position = end + bytes.Index(document[end:], []byte("endstream")) + len("endstream")
	}
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
