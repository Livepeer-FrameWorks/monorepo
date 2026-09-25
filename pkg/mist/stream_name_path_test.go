package mist

import "testing"

func TestDecodeStreamNamePathAcceptsEveryWireForm(t *testing.T) {
	cases := map[string]string{
		"live+name":            "live+name",
		"live%2Bname":          "live+name",
		"live%2bname":          "live+name",
		"live%252bname":        "live+name",
		"live%252Bname-AbC123": "live+name-AbC123",
		"processing%252babc12": "processing+abc12",
		"plainname_1.2-3":      "plainname_1.2-3",
	}
	for segment, want := range cases {
		got, err := DecodeStreamNamePath(segment)
		if err != nil || got != want {
			t.Errorf("DecodeStreamNamePath(%q) = %q, %v; want %q", segment, got, err, want)
		}
	}
}

func TestDecodeStreamNamePathRejectsMalformedAndForeignSegments(t *testing.T) {
	for _, segment := range []string{
		"",
		"live%zzname",       // malformed escape
		"live%25252bname",   // more encoding layers than any hop produces
		"live%2Fname",       // decodes to a path separator
		"live name",         // outside the stream-name alphabet
		"+name",             // empty base
		"live+",             // empty wildcard suffix
		"live%2Bname%2Bx",   // two wildcard separators
		"..%2F..%2Fetc",     // traversal once decoded
		"live%2Bname%3Fx=y", // query smuggled into the name
	} {
		if got, err := DecodeStreamNamePath(segment); err == nil {
			t.Errorf("DecodeStreamNamePath(%q) = %q, want error", segment, got)
		}
	}
}

func TestEncodeStreamNamePathRoundTrips(t *testing.T) {
	for _, name := range []string{"live+abc", "processing+0123abcd", "vod-1.2_x"} {
		got, err := DecodeStreamNamePath(EncodeStreamNamePath(name))
		if err != nil || got != name {
			t.Errorf("round trip %q = %q, %v", name, got, err)
		}
	}
}
