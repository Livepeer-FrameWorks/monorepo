package streamident

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// RuntimeRole distinguishes a publisher input from one destination's relay.
// Both use Mist's existing live wildcard; their concrete names never overlap.
type RuntimeRole string

const (
	RuntimePublisher RuntimeRole = "p"
	RuntimeRelay     RuntimeRole = "r"
)

type RuntimeBinding struct {
	InternalName string
	Role         RuntimeRole
	ExecutionID  string
}

// BoundRuntimeName is a deterministic codec, not an authorization token. The
// session or accepted-pull record must independently verify this exact binding.
func BoundRuntimeName(internal string, role RuntimeRole, executionID string) (string, error) {
	if role != RuntimePublisher && role != RuntimeRelay {
		return "", errors.New("unknown media runtime role")
	}
	if internal == "" || strings.IndexFunc(internal, func(r rune) bool {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		return !allowed
	}) >= 0 {
		return "", errors.New("invalid media runtime identity")
	}
	id, err := uuid.Parse(executionID)
	if err != nil || id == uuid.Nil || id.String() != executionID {
		return "", errors.New("media runtime requires a canonical execution UUID")
	}
	name := "live+" + internal + "_fw1" + string(role) + "_" + strings.ReplaceAll(executionID, "-", "")
	if len(name) > 100 {
		return "", errors.New("media runtime exceeds Mist's name limit")
	}
	return name, nil
}

func ParseRuntimeBinding(name string) (RuntimeBinding, bool) {
	body, ok := strings.CutPrefix(name, "live+")
	if !ok || len(body) < 39 || len(name) > 100 {
		return RuntimeBinding{}, false
	}
	marker := body[len(body)-38 : len(body)-32]
	if marker != "_fw1p_" && marker != "_fw1r_" {
		return RuntimeBinding{}, false
	}
	rawID := body[len(body)-32:]
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil {
		return RuntimeBinding{}, false
	}
	binding := RuntimeBinding{InternalName: body[:len(body)-38], Role: RuntimeRole(marker[4:5]), ExecutionID: id.String()}
	canonical, err := BoundRuntimeName(binding.InternalName, binding.Role, binding.ExecutionID)
	return binding, err == nil && canonical == name
}
