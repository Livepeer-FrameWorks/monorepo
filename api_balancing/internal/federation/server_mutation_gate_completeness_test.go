package federation

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"

	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// FEDERATION_ENABLED disables and enables the whole inbound cross-cluster
// surface as one unit, and its zero value is fail-closed. Spot-checking a few
// RPCs cannot hold that line: an RPC added later without the gate ships open
// while every existing test still passes.
//
// So this walks the service descriptor itself. Every method the proto declares
// must refuse when the gate is off, which makes forgetting the check a test
// failure at the moment the RPC is added rather than a finding months later.
func TestEveryFederationRPCRefusesWhenMutationsDisabled(t *testing.T) {
	// AllowFederationMutations omitted: the fail-closed zero value.
	srv := NewFederationServer(FederationServerConfig{Logger: testLogger(), ClusterID: "cluster-a"})
	ctx := serviceAuthContext()

	methods := foghornfederationpb.File_foghorn_federation_proto.
		Services().ByName("FoghornFederation").Methods()
	if methods.Len() == 0 {
		t.Fatal("no methods found on the FoghornFederation service descriptor")
	}

	srvValue := reflect.ValueOf(srv)
	checked := 0
	for i := 0; i < methods.Len(); i++ {
		method := methods.Get(i)
		name := string(method.Name())
		t.Run(name, func(t *testing.T) {
			handler := srvValue.MethodByName(name)
			if !handler.IsValid() {
				t.Fatalf("proto declares %s but FederationServer does not implement it", name)
			}
			refused, detail := callRefuses(t, handler, ctx, method)
			if !refused {
				t.Fatalf("%s did not refuse with mutations disabled: %s", name, detail)
			}
		})
		checked++
	}
	if checked < 13 {
		t.Fatalf("only walked %d methods; the descriptor lookup is not seeing the service", checked)
	}
}

// callRefuses invokes one RPC with zero-valued input and reports whether it
// refused. Streaming methods take a server stream rather than a request, and a
// refusal there is an error return before the first Recv.
func callRefuses(t *testing.T, handler reflect.Value, ctx context.Context, method protoreflect.MethodDescriptor) (bool, string) {
	t.Helper()
	handlerType := handler.Type()

	if method.IsStreamingClient() || method.IsStreamingServer() {
		stream := &gateProbeStream{ctx: ctx}
		out := handler.Call([]reflect.Value{reflect.ValueOf(stream)})
		err, _ := out[len(out)-1].Interface().(error)
		if err == nil {
			return false, "returned nil error"
		}
		if stream.recvCalled {
			return false, "read from the stream before refusing"
		}
		return isRefusal(err), err.Error()
	}

	// Unary: (ctx, *Request) (*Response, error). The request is the zero value —
	// the gate must refuse before any field of it is considered.
	reqType := handlerType.In(1)
	args := []reflect.Value{reflect.ValueOf(ctx), reflect.New(reqType.Elem())}
	out := handler.Call(args)
	err, _ := out[1].Interface().(error)
	if err != nil {
		return isRefusal(err), err.Error()
	}
	// Some RPCs report refusal in the response body rather than as an error, so
	// that a caller can distinguish "not allowed here" from a transport fault.
	resp := out[0]
	if resp.IsNil() {
		return false, "nil response and nil error"
	}
	// Some RPCs carry the refusal in Reason, others in Error, so the marker is
	// looked for across the response's string fields rather than in one named
	// place. It must be the same marker either way: a caller telling a policy
	// refusal apart from a fault has only the string to go on.
	body := resp.Elem()
	carried := false
	for i := 0; i < body.NumField(); i++ {
		field := body.Field(i)
		if field.Kind() == reflect.String && strings.Contains(field.String(), gateRefusalMarker) {
			carried = true
			break
		}
	}
	if !carried {
		return false, "returned a response with no refusal"
	}
	for _, name := range []string{"Accepted", "Handled"} {
		if f := body.FieldByName(name); f.IsValid() && f.Kind() == reflect.Bool && f.Bool() {
			return false, "reported the disabled marker but still set " + name
		}
	}
	return true, ""
}

// gateRefusalMarker is the single string every refusal must carry, whether it
// arrives as a gRPC status or in a response field.
const gateRefusalMarker = "federation_mutations_disabled"

func isRefusal(err error) bool {
	return strings.Contains(err.Error(), gateRefusalMarker)
}

// gateProbeStream stands in for a server stream and records whether the handler
// tried to read before refusing.
type gateProbeStream struct {
	grpc.ServerStream
	ctx        context.Context
	recvCalled bool
}

func (s *gateProbeStream) Context() context.Context { return s.ctx }

func (s *gateProbeStream) Recv() (*foghornfederationpb.PeerMessage, error) {
	s.recvCalled = true
	return nil, context.Canceled
}

func (s *gateProbeStream) Send(*foghornfederationpb.PeerMessage) error { return nil }

func (s *gateProbeStream) SendMsg(any) error { return nil }

func (s *gateProbeStream) RecvMsg(any) error {
	s.recvCalled = true
	return context.Canceled
}
