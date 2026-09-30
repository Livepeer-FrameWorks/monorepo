package cmd

import (
	"bytes"
	"encoding/json"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// protoOutputJSON is the --output json form of an RPC response: proto JSON
// with the proto field names and every field present, so an empty list
// prints as [] and an unset message as null instead of the key vanishing.
// encoding/json would drop them through the generated omitempty tags and
// print an empty response as {}.
var protoOutputJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// writeProtoJSON writes m to w as indented proto JSON followed by a newline.
func writeProtoJSON(w io.Writer, m proto.Message) error {
	body, err := protoOutputJSON.Marshal(m)
	if err != nil {
		return err
	}
	// protojson output whitespace is deliberately unstable; json.Indent
	// rewrites it into one stable layout.
	var out bytes.Buffer
	if err = json.Indent(&out, body, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err = w.Write(out.Bytes())
	return err
}
