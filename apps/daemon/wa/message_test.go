package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// isRevoke mirrors the guard in onMessage. ProtocolMessage_REVOKE is the zero
// value of the type enum, so a nil protocol message reports REVOKE too and the
// payload itself has to be checked first.
func isRevoke(msg *waE2E.Message) bool {
	protoMsg := msg.GetProtocolMessage()
	return protoMsg != nil && protoMsg.GetType() == waE2E.ProtocolMessage_REVOKE
}

func TestIsRevokeIgnoresOrdinaryMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  *waE2E.Message
		want bool
	}{
		{
			name: "plain text",
			msg:  &waE2E.Message{Conversation: proto.String("halo")},
		},
		{
			name: "reply",
			msg: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("halo"),
			}},
		},
		{
			name: "image",
			msg:  &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}},
		},
		{
			name: "revoke",
			msg: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				Key:  &waCommon.MessageKey{ID: proto.String("ABC")},
			}},
			want: true,
		},
		{
			name: "non-revoke protocol message",
			msg: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(),
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRevoke(tc.msg); got != tc.want {
				t.Fatalf("isRevoke = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMessageContextReadsQuoteFromEveryCarrier(t *testing.T) {
	ctxInfo := func() *waE2E.ContextInfo {
		return &waE2E.ContextInfo{StanzaID: proto.String("QUOTED")}
	}

	tests := []struct {
		name string
		msg  *waE2E.Message
		want string
	}{
		{name: "plain text has no context", msg: &waE2E.Message{Conversation: proto.String("hi")}},
		{
			name: "extended text",
			msg:  &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{ContextInfo: ctxInfo()}},
			want: "QUOTED",
		},
		{
			name: "image",
			msg:  &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: ctxInfo()}},
			want: "QUOTED",
		},
		{
			name: "document",
			msg:  &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{ContextInfo: ctxInfo()}},
			want: "QUOTED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageContext(tc.msg).GetStanzaID(); got != tc.want {
				t.Fatalf("stanza id = %q, want %q", got, tc.want)
			}
		})
	}
}
