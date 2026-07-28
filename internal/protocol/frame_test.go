package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestFrameRoundTripAndUnknownFieldRejection(t *testing.T) {
	var buffer bytes.Buffer
	input := ClientHello{Version: "2.0", ClientPublic: "key", Capabilities: []string{"raw"}}
	if err := WriteFrame(&buffer, input); err != nil {
		t.Fatal(err)
	}
	var output ClientHello
	if err := ReadFrame(&buffer, &output); err != nil {
		t.Fatal(err)
	}
	if output.Version != input.Version || output.ClientPublic != input.ClientPublic {
		t.Fatalf("unexpected output: %#v", output)
	}

	payload := `{"version":"2.0","client_public":"key","capabilities":[],"role":"admin"}`
	buffer.Reset()
	_ = binary.Write(&buffer, binary.BigEndian, uint32(len(payload)))
	buffer.WriteString(payload)
	if err := ReadFrame(&buffer, &output); err == nil {
		t.Fatal("unknown security field accepted")
	}
}

func TestFrameRejectsOversizeBeforeAllocation(t *testing.T) {
	var buffer bytes.Buffer
	_ = binary.Write(&buffer, binary.BigEndian, uint32(MaxFrame+1))
	if err := ReadFrame(&buffer, &ClientHello{}); err == nil || !strings.Contains(err.Error(), "invalid frame size") {
		t.Fatalf("unexpected error: %v", err)
	}
}
