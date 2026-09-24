package events

import (
	"slices"
	"strings"
	"testing"

	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// catalog is the event catalog decided in PLAN_PLATFORM_LESSONS.md. The
// registry must hold exactly these types with this visibility and scope.
var catalog = map[string]struct {
	public   bool
	platform bool
}{
	"stream.created":                 {public: true},
	"stream.updated":                 {public: true},
	"stream.deleted":                 {public: true},
	"stream.key_rotated":             {public: true},
	"stream.connected":               {public: true},
	"stream.live":                    {public: true},
	"stream.idle":                    {public: true},
	"clip.requested":                 {public: true},
	"clip.ready":                     {public: true},
	"clip.failed":                    {public: true},
	"recording.started":              {public: true},
	"recording.stopped":              {public: true},
	"recording.ready":                {public: true},
	"recording.failed":               {public: true},
	"upload.created":                 {public: true},
	"upload.completed":               {public: true},
	"upload.aborted":                 {public: true},
	"upload.ready":                   {public: true},
	"upload.failed":                  {public: true},
	"multistream.status_changed":     {public: true},
	"api_token.created":              {public: true},
	"api_token.revoked":              {public: true},
	"billing.invoice_created":        {public: true},
	"billing.invoice_paid":           {public: true},
	"billing.payment_failed":         {public: true},
	"billing.topup_credited":         {public: true},
	"billing.details_updated":        {public: true},
	"account.suspended":              {public: true},
	"custom_domain.verified":         {public: true},
	"custom_domain.failed":           {public: true},
	"tenant.created":                 {},
	"tenant.updated":                 {},
	"tenant.deleted":                 {},
	"tenant.cluster_assigned":        {},
	"tenant.cluster_unassigned":      {},
	"cluster.created":                {platform: true},
	"cluster.updated":                {platform: true},
	"cluster.invite_created":         {},
	"cluster.invite_revoked":         {},
	"cluster.subscription_requested": {},
	"cluster.subscription_approved":  {},
	"cluster.subscription_rejected":  {},
	"recording.chapter_ready":        {},
	"artifact.node_copy_changed":     {},
	"billing.payment_created":        {},
	"billing.subscription_created":   {},
	"billing.subscription_updated":   {},
	"webhook.endpoint_auto_disabled": {},
}

func TestRegistryHoldsTheDecidedCatalog(t *testing.T) {
	specs := Specs()
	if len(specs) != len(catalog) {
		var got []string
		for _, s := range specs {
			got = append(got, s.Type)
		}
		t.Fatalf("registry has %d types, catalog has %d: %v", len(specs), len(catalog), got)
	}
	for _, spec := range specs {
		want, ok := catalog[spec.Type]
		if !ok {
			t.Fatalf("registry type %q is not in the catalog", spec.Type)
		}
		if spec.Public() != want.public {
			t.Fatalf("%s public = %v, want %v", spec.Type, spec.Public(), want.public)
		}
		if (spec.Scope == eventspb.Scope_SCOPE_PLATFORM) != want.platform {
			t.Fatalf("%s scope = %s", spec.Type, spec.Scope)
		}
		pkg := spec.MessageName.Parent()
		if spec.Public() && pkg != PublicPackage || !spec.Public() && pkg != InternalPackage {
			t.Fatalf("%s is declared by %s in package %s", spec.Type, spec.MessageName, pkg)
		}
		if bySpec, _ := SpecFor(spec.NewMessage()); bySpec.Type != spec.Type {
			t.Fatalf("SpecFor(%s) = %q", spec.MessageName, bySpec.Type)
		}
	}
}

// bannedPublicField lists field-name words that indicate internal data: Mist
// internal names, node identity, storage locations, network addresses,
// credentials, and raw error text.
var bannedPublicField = []string{"internal", "node", "path", "url", "uri", "ip", "error", "s3", "bucket", "host", "key", "secret", "password"}

func TestPublicEventsCarryNoInternalFields(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor, path string)
	walk = func(md protoreflect.MessageDescriptor, path string) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		if md.ParentFile().Package() != PublicPackage && !strings.HasPrefix(string(md.FullName()), "google.protobuf.") {
			t.Fatalf("%s: public event references non-public message %s", path, md.FullName())
		}
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			name := string(fd.Name())
			words := strings.Split(name, "_")
			for _, banned := range bannedPublicField {
				if slices.Contains(words, banned) {
					t.Fatalf("%s.%s: public field name contains %q", path, name, banned)
				}
			}
			if fd.Message() != nil {
				walk(fd.Message(), path+"."+name)
			}
		}
	}
	for _, spec := range Specs() {
		if spec.Public() {
			walk(spec.NewMessage().ProtoReflect().Descriptor(), spec.Type)
		}
	}
}

func syntheticFile(t *testing.T, pkg string, messages map[string]*eventspb.EventSpec) protoreflect.FileDescriptor {
	t.Helper()
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String(strings.ReplaceAll(pkg, ".", "/") + "/synthetic.proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"events/options.proto"},
	}
	names := make([]string, 0, len(messages))
	for name := range messages {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		opts := &descriptorpb.MessageOptions{}
		proto.SetExtension(opts, eventspb.E_Event, messages[name])
		fdp.MessageType = append(fdp.MessageType, &descriptorpb.DescriptorProto{
			Name: proto.String(name),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("id"), Number: proto.Int32(1), JsonName: proto.String("id"),
				Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			}},
			Options: opts,
		})
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func publicSpec(eventType string) *eventspb.EventSpec {
	return &eventspb.EventSpec{Type: eventType, Visibility: eventspb.Visibility_VISIBILITY_PUBLIC, Scope: eventspb.Scope_SCOPE_TENANT, Aggregate: "things"}
}

func TestRegistryRejectsDuplicateTypes(t *testing.T) {
	packages := map[protoreflect.FullName]eventspb.Visibility{"test.events.v1": eventspb.Visibility_VISIBILITY_PUBLIC}
	distinct := syntheticFile(t, "test.events.v1", map[string]*eventspb.EventSpec{
		"ThingCreated": publicSpec("thing.created"),
		"ThingDeleted": publicSpec("thing.deleted"),
	})
	reg, err := buildRegistry([]protoreflect.FileDescriptor{distinct}, packages)
	if err != nil {
		t.Fatalf("distinct types rejected: %v", err)
	}
	if spec, ok := reg.Lookup("thing.deleted"); !ok || spec.MessageName != "test.events.v1.ThingDeleted" {
		t.Fatalf("Lookup(thing.deleted) = %+v, %v", spec, ok)
	}

	duplicate := syntheticFile(t, "test.events.v1", map[string]*eventspb.EventSpec{
		"ThingCreated":      publicSpec("thing.created"),
		"ThingCreatedAgain": publicSpec("thing.created"),
	})
	if _, err := buildRegistry([]protoreflect.FileDescriptor{duplicate}, packages); err == nil ||
		!strings.Contains(err.Error(), `event type "thing.created" declared by both`) {
		t.Fatalf("duplicate type error = %v", err)
	}
}

func TestRegistryRejectsInvalidAnnotations(t *testing.T) {
	packages := map[protoreflect.FullName]eventspb.Visibility{"test.events.v1": eventspb.Visibility_VISIBILITY_PUBLIC}
	for name, spec := range map[string]*eventspb.EventSpec{
		"visibility": {Type: "thing.created", Visibility: eventspb.Visibility_VISIBILITY_INTERNAL, Scope: eventspb.Scope_SCOPE_TENANT, Aggregate: "things"},
		"scope":      {Type: "thing.created", Visibility: eventspb.Visibility_VISIBILITY_PUBLIC, Aggregate: "things"},
		"type":       {Type: "ThingCreated", Visibility: eventspb.Visibility_VISIBILITY_PUBLIC, Scope: eventspb.Scope_SCOPE_TENANT, Aggregate: "things"},
		"aggregate":  {Type: "thing.created", Visibility: eventspb.Visibility_VISIBILITY_PUBLIC, Scope: eventspb.Scope_SCOPE_TENANT},
	} {
		fd := syntheticFile(t, "test.events.v1", map[string]*eventspb.EventSpec{"Thing": spec})
		if _, err := buildRegistry([]protoreflect.FileDescriptor{fd}, packages); err == nil {
			t.Fatalf("%s: invalid annotation accepted", name)
		}
	}
	outside := syntheticFile(t, "test.elsewhere.v1", map[string]*eventspb.EventSpec{"Thing": publicSpec("thing.created")})
	if _, err := buildRegistry([]protoreflect.FileDescriptor{outside}, packages); err == nil {
		t.Fatal("event message outside an event package accepted")
	}
}
