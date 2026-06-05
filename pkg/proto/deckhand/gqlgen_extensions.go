// GraphQL enum marshaling for deckhand proto types.

package deckhandpb

import (
	"fmt"
	"io"
	"strconv"
)

// MarshalGQL implements the graphql.Marshaler interface for ConversationStatus
func (e ConversationStatus) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case ConversationStatus_CONVERSATION_STATUS_OPEN:
		s = "OPEN"
	case ConversationStatus_CONVERSATION_STATUS_RESOLVED:
		s = "RESOLVED"
	case ConversationStatus_CONVERSATION_STATUS_PENDING:
		s = "PENDING"
	default:
		s = "OPEN"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for ConversationStatus
func (e *ConversationStatus) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "OPEN":
		*e = ConversationStatus_CONVERSATION_STATUS_OPEN
	case "RESOLVED":
		*e = ConversationStatus_CONVERSATION_STATUS_RESOLVED
	case "PENDING":
		*e = ConversationStatus_CONVERSATION_STATUS_PENDING
	default:
		return fmt.Errorf("%s is not a valid ConversationStatus", str)
	}
	return nil
}

// MarshalGQL implements the graphql.Marshaler interface for MessageSender
func (e MessageSender) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case MessageSender_MESSAGE_SENDER_USER:
		s = "USER"
	case MessageSender_MESSAGE_SENDER_AGENT:
		s = "AGENT"
	case MessageSender_MESSAGE_SENDER_SYSTEM:
		s = "SYSTEM"
	default:
		s = "USER"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for MessageSender
func (e *MessageSender) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "USER":
		*e = MessageSender_MESSAGE_SENDER_USER
	case "AGENT":
		*e = MessageSender_MESSAGE_SENDER_AGENT
	case "SYSTEM":
		*e = MessageSender_MESSAGE_SENDER_SYSTEM
	default:
		return fmt.Errorf("%s is not a valid MessageSender", str)
	}
	return nil
}
