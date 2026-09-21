package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func field(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	if repeated {
		label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	}
	f := &descriptorpb.FieldDescriptorProto{
		Name: proto.String(name), Number: proto.Int32(number), Type: kind.Enum(), Label: label.Enum(),
		JsonName: proto.String(jsonName(name)),
	}
	if typeName != "" {
		f.TypeName = proto.String(typeName)
	}
	return f
}

func jsonName(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

const (
	tString   = descriptorpb.FieldDescriptorProto_TYPE_STRING
	tInt64    = descriptorpb.FieldDescriptorProto_TYPE_INT64
	tInt32    = descriptorpb.FieldDescriptorProto_TYPE_INT32
	tBool     = descriptorpb.FieldDescriptorProto_TYPE_BOOL
	tDouble   = descriptorpb.FieldDescriptorProto_TYPE_DOUBLE
	tUint64   = descriptorpb.FieldDescriptorProto_TYPE_UINT64
	tBytes    = descriptorpb.FieldDescriptorProto_TYPE_BYTES
	tEnum     = descriptorpb.FieldDescriptorProto_TYPE_ENUM
	tMessage  = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	goPackage = "example.com/events/v1;eventsv1"
)

// syntheticFile builds a public event package that exercises every mapping:
// ID and String, Int from int32 and int64, Boolean, Float, repeated scalars, an
// enum, a submessage, and a Timestamp.
func syntheticFile(t *testing.T, extra func(*descriptorpb.FileDescriptorProto)) protoreflect.FileDescriptor {
	t.Helper()
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test/events/v1/events.proto"),
		Package:    proto.String("test.events.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		Options:    &descriptorpb.FileOptions{GoPackage: proto.String(goPackage)},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Color"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("COLOR_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("COLOR_RED"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Thing"), Field: []*descriptorpb.FieldDescriptorProto{
				field("thing_id", 1, tString, "", false),
				field("color", 2, tEnum, ".test.events.v1.Color", false),
			}},
			{Name: proto.String("WidgetMade"), Field: []*descriptorpb.FieldDescriptorProto{
				field("widget_id", 1, tString, "", false),
				field("label", 2, tString, "", false),
				field("count", 3, tInt32, "", false),
				field("size_bytes", 4, tInt64, "", false),
				field("enabled", 5, tBool, "", false),
				field("ratio", 6, tDouble, "", false),
				field("tags", 7, tString, "", true),
				field("thing", 8, tMessage, ".test.events.v1.Thing", false),
				field("made_at", 9, tMessage, ".google.protobuf.Timestamp", false),
			}},
			{Name: proto.String("WidgetDropped"), Field: []*descriptorpb.FieldDescriptorProto{
				field("widget_id", 1, tString, "", false),
				field("colors", 2, tEnum, ".test.events.v1.Color", true),
			}},
		},
	}
	if extra != nil {
		extra(fdp)
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build synthetic file: %v", err)
	}
	return fd
}

func syntheticEvents(fd protoreflect.FileDescriptor) []eventMessage {
	return []eventMessage{
		{Type: "widget.made", Aggregate: "widgets", Desc: fd.Messages().ByName("WidgetMade")},
		{Type: "widget.dropped", Aggregate: "widgets", Desc: fd.Messages().ByName("WidgetDropped")},
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from the golden file; got:\n%s", name, got)
	}
}

func TestGenerateGolden(t *testing.T) {
	out, err := generate(syntheticEvents(syntheticFile(t, nil)))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	checkGolden(t, "schema.graphql.golden", out.Schema)
	checkGolden(t, "models.yml.golden", out.Models)
}

func TestGenerateRejectsUnmappableFields(t *testing.T) {
	cases := map[string]func(*descriptorpb.FileDescriptorProto){
		"uint64 fields have no GraphQL mapping": func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType[1].Field = append(f.MessageType[1].Field, field("total", 20, tUint64, "", false))
		},
		"bytes fields have no GraphQL mapping": func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType[1].Field = append(f.MessageType[1].Field, field("blob", 20, tBytes, "", false))
		},
		"oneof fields are not supported": func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType[1].OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("choice")}}
			one := field("pick", 20, tString, "", false)
			one.OneofIndex = proto.Int32(0)
			f.MessageType[1].Field = append(f.MessageType[1].Field, one)
		},
		"nested type": func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType[1].EnumType = []*descriptorpb.EnumDescriptorProto{{
				Name:  proto.String("Inner"),
				Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("INNER_UNSPECIFIED"), Number: proto.Int32(0)}},
			}}
			f.MessageType[1].Field = append(f.MessageType[1].Field, field("inner", 20, tEnum, ".test.events.v1.WidgetMade.Inner", false))
		},
	}
	for want, mutate := range cases {
		t.Run(want, func(t *testing.T) {
			// Each case registers nothing globally, so the same file name is reusable.
			_, err := generate(syntheticEvents(syntheticFile(t, mutate)))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("generate error = %v, want one containing %q", err, want)
			}
		})
	}
}

func TestRenderRefusesHandWrittenTypeOfTheSameName(t *testing.T) {
	schema := "type WidgetMade {\n  id: ID!\n}\n\n" + schemaBegin + "\n" + schemaEnd + "\n"
	models := "models:\n" + modelsBegin + "\n" + modelsEnd + "\n"
	_, _, err := render(schema, models, syntheticEvents(syntheticFile(t, nil)))
	if err == nil || !strings.Contains(err.Error(), "WidgetMade") {
		t.Fatalf("render error = %v, want a clash on WidgetMade", err)
	}
}

func TestSpliceReplacesOnlyTheBlock(t *testing.T) {
	content := "before\n" + schemaBegin + "\nstale\n" + schemaEnd + "\nafter\n"
	got, err := splice(content, schemaBegin, schemaEnd, "fresh\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "before\n" + schemaBegin + "\nfresh\n" + schemaEnd + "\nafter\n"; got != want {
		t.Fatalf("splice = %q, want %q", got, want)
	}
	if _, err := splice("no markers\n", schemaBegin, schemaEnd, "x\n"); err == nil {
		t.Fatal("splice without markers succeeded")
	}
	twice := content + schemaBegin + "\n" + schemaEnd + "\n"
	if _, err := splice(twice, schemaBegin, schemaEnd, "x\n"); err == nil {
		t.Fatal("splice with a repeated marker succeeded")
	}
}

// -check reports a block that no longer matches the registry and writes
// nothing; a normal run restores it.
func TestCheckReportsEditedBlock(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	for _, rel := range []string{schemaPath, modelsPath} {
		raw, readErr := os.ReadFile(filepath.Join(repo, rel))
		if readErr != nil {
			t.Fatal(readErr)
		}
		content := string(raw)
		if rel == schemaPath {
			content = strings.Replace(content, "type StreamLive {\n  streamId: ID!\n}", "type StreamLive {\n  streamId: ID!\n  nodeId: String\n}", 1)
		}
		if mkErr := os.MkdirAll(filepath.Dir(filepath.Join(tmp, rel)), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if writeErr := os.WriteFile(filepath.Join(tmp, rel), []byte(content), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	edited, err := os.ReadFile(filepath.Join(tmp, schemaPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "nodeId: String") {
		t.Fatal("test setup did not edit the StreamLive block")
	}

	stale, err := run(tmp, true)
	if err != nil {
		t.Fatalf("check run: %v", err)
	}
	if len(stale) != 1 || !strings.HasSuffix(stale[0], schemaPath) {
		t.Fatalf("stale = %v, want only %s", stale, schemaPath)
	}
	if after, _ := os.ReadFile(filepath.Join(tmp, schemaPath)); string(after) != string(edited) {
		t.Fatal("-check rewrote the schema")
	}

	if _, err := run(tmp, false); err != nil {
		t.Fatalf("write run: %v", err)
	}
	if stale, err := run(tmp, true); err != nil || len(stale) != 0 {
		t.Fatalf("after regeneration stale = %v, err = %v", stale, err)
	}
}

// The committed blocks in pkg/graphql/schema.graphql and api_gateway/gqlgen.yml
// equal what the linked event registry generates, and cover every public type.
func TestRepositoryBlocksMatchRegistry(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := run(repo, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(stale) > 0 {
		t.Fatalf("stale generated blocks in %v; run make graphql-events", stale)
	}

	raw, err := os.ReadFile(filepath.Join(repo, schemaPath))
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	evs := registryEvents()
	if len(evs) == 0 {
		t.Fatal("registry has no public events")
	}
	for _, ev := range evs {
		name := string(ev.Desc.Name())
		if !strings.Contains(schema, "\n  | "+name+"\n") || !strings.Contains(schema, "\ntype "+name+" {\n") {
			t.Errorf("public event %s (%s) is missing from the schema block or the union", ev.Type, name)
		}
	}
}
