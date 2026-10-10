//nolint:testpackage // Exercise protocol handlers without a real clipboard or serial device.
package vdagent

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/cirruslabs/tart-guest-agent/internal/spice/vd"
	"github.com/cirruslabs/tart-guest-agent/internal/spice/vdi"
	"github.com/stretchr/testify/require"
)

const (
	hostClipboardText           = "host text"
	terminatedHostClipboardText = "host text\x00"
)

func newClipboardTestAgent() (*VDAgent, *bytes.Buffer) {
	wire := &bytes.Buffer{}
	agent := &VDAgent{
		vdi: vdi.New(wire),
		writeClipboard: func(context.Context, []byte) error {
			return nil
		},
	}

	return agent, wire
}

func TestHostClipboardDoesNotEcho(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		watched string
	}{
		{name: "plain", host: hostClipboardText, watched: hostClipboardText},
		{name: "unicode", host: "\u4e2d\u6587\U0001f642", watched: "\u4e2d\u6587\U0001f642"},
		{name: "host terminator", host: terminatedHostClipboardText, watched: hostClipboardText},
		{name: "watcher terminator", host: hostClipboardText, watched: terminatedHostClipboardText},
		{name: "multiple terminators", host: "host text\x00\x00", watched: terminatedHostClipboardText},
		{name: "empty", host: "", watched: ""},
		{name: "empty terminated", host: "\x00", watched: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			agent, wire := newClipboardTestAgent()
			agent.lastClipboardState = []byte("previous guest text")
			require.NoError(t, agent.processHostClipboard(t.Context(), []byte(testCase.host)))
			require.NoError(t, agent.processClipboardState([]byte(testCase.watched)))
			require.Zero(t, wire.Len(), "host-originated text must not produce a new clipboard grab")
		})
	}
}

func TestHostClipboardSnapshotOwnsItsData(t *testing.T) {
	agent, wire := newClipboardTestAgent()
	data := []byte(hostClipboardText)
	require.NoError(t, agent.processHostClipboard(t.Context(), data))
	data[0] = 'X'

	require.NoError(t, agent.processClipboardState([]byte(hostClipboardText)))
	require.Zero(t, wire.Len(), "reusing the received buffer must not cause an echo")
}

func TestHostClipboardWritePreservesPayloadAndContext(t *testing.T) {
	agent, _ := newClipboardTestAgent()
	ctx := t.Context()
	var written []byte
	agent.writeClipboard = func(receivedContext context.Context, data []byte) error {
		require.Equal(t, ctx, receivedContext)
		written = bytes.Clone(data)
		return nil
	}

	require.NoError(t, agent.processHostClipboard(ctx, []byte("received text\x00")))
	require.Equal(t, []byte("received text\x00"), written,
		"normalizing comparisons must not alter the clipboard write payload")
}

func TestGuestCopyStillAdvertised(t *testing.T) {
	agent, wire := newClipboardTestAgent()
	require.NoError(t, agent.processHostClipboard(t.Context(), []byte(hostClipboardText)))
	require.NoError(t, agent.processClipboardState([]byte(hostClipboardText)))
	require.Zero(t, wire.Len())

	require.NoError(t, agent.processClipboardState([]byte("new guest text")))
	message, err := vd.ReadVDAgentMessage(vdi.New(wire))
	require.NoError(t, err)
	require.Equal(t, uint32(vd.VD_AGENT_PROTOCOL), message.Protocol)
	require.Equal(t, uint32(vd.VD_AGENT_CLIPBOARD_GRAB), message.Type)
	grab, err := vd.DecodeVDAgentClipboardGrab(bytes.NewReader(message.Data))
	require.NoError(t, err)
	require.Equal(t, uint8(vd.VD_AGENT_CLIPBOARD_SELECTION_CLIPBOARD), grab.Selection)
	require.Equal(t, uint32(vd.VD_AGENT_CLIPBOARD_UTF8_TEXT), grab.Type)
	require.Zero(t, wire.Len(), "a new guest copy must send exactly one framed grab")
}

func TestClipboardWriteErrorPreservesState(t *testing.T) {
	agent, wire := newClipboardTestAgent()
	agent.lastClipboardState = []byte("previous text")
	agent.writeClipboard = func(context.Context, []byte) error {
		return os.ErrPermission
	}

	err := agent.processHostClipboard(t.Context(), []byte("failed host write"))
	require.ErrorIs(t, err, os.ErrPermission)
	require.NoError(t, agent.processClipboardState([]byte("previous text")))
	require.Zero(t, wire.Len(), "a failed write must leave the previous known state intact")

	require.NoError(t, agent.processClipboardState([]byte("failed host write")))
	message, err := vd.ReadVDAgentMessage(vdi.New(wire))
	require.NoError(t, err)
	require.Equal(t, uint32(vd.VD_AGENT_CLIPBOARD_GRAB), message.Type,
		"a failed host write must not suppress a later guest copy of that text")
}

func TestClipboardNormalizationPreservesTextChanges(t *testing.T) {
	cases := []struct {
		name  string
		host  string
		guest string
	}{
		{name: "different text", host: hostClipboardText, guest: "guest text"},
		{name: "embedded NUL", host: "a\x00b", guest: "ab"},
		{name: "leading NUL", host: "\x00a", guest: "a"},
		{name: "whitespace", host: "text", guest: "text "},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			agent, wire := newClipboardTestAgent()
			require.NoError(t, agent.processHostClipboard(t.Context(), []byte(testCase.host)))
			require.NoError(t, agent.processClipboardState([]byte(testCase.guest)))
			message, err := vd.ReadVDAgentMessage(vdi.New(wire))
			require.NoError(t, err)
			require.Equal(t, uint32(vd.VD_AGENT_CLIPBOARD_GRAB), message.Type)
		})
	}
}
