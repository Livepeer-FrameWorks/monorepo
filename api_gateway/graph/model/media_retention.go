package model

import (
	"fmt"
	"io"
	"strconv"
)

// MediaRetentionTarget is the GraphQL string enum for the asset class
// targeted by an override / reset operation. Hand-defined here (rather than
// modelgen'd) because the proto package autobinds its own int32-based
// MediaRetentionTarget and gqlgen can't autobind both sides as the same
// GraphQL enum. The resolver translates between this string enum and the
// proto enum at the wire boundary.
type MediaRetentionTarget string

const (
	MediaRetentionTargetDvr  MediaRetentionTarget = "DVR"
	MediaRetentionTargetClip MediaRetentionTarget = "CLIP"
	MediaRetentionTargetVod  MediaRetentionTarget = "VOD"
)

var AllMediaRetentionTarget = []MediaRetentionTarget{
	MediaRetentionTargetDvr,
	MediaRetentionTargetClip,
	MediaRetentionTargetVod,
}

func (e MediaRetentionTarget) IsValid() bool {
	switch e {
	case MediaRetentionTargetDvr, MediaRetentionTargetClip, MediaRetentionTargetVod:
		return true
	}
	return false
}

func (e MediaRetentionTarget) String() string { return string(e) }

func (e *MediaRetentionTarget) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	*e = MediaRetentionTarget(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid MediaRetentionTarget", str)
	}
	return nil
}

func (e MediaRetentionTarget) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprint(w, strconv.Quote(string(e)))
}
