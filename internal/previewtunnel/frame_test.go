package previewtunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func rawFrame(frameType byte, streamID uint32, payload []byte) []byte {
	message := make([]byte, 5, 5+len(payload))
	message[0] = frameType
	binary.BigEndian.PutUint32(message[1:5], streamID)
	return append(message, payload...)
}

func creditPayload(credit uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, credit)
	return payload
}

func jsonUnit(unit string) string {
	return `\` + "u" + unit
}

func openPayloadOfSize(size int) []byte {
	base := `{"preview_id":"p","preview_key":""}`
	return []byte(`{"preview_id":"p","preview_key":"` + strings.Repeat("k", size-len(base)) + `"}`)
}

func TestFrameRoundTripForEachType(t *testing.T) {
	cases := []Frame{
		{Type: FrameOpen, StreamID: 1, Payload: []byte(`{"preview_id":"prv_1","preview_key":"key_1"}`)},
		{Type: FrameData, StreamID: 3, Payload: []byte("hello")},
		{Type: FrameEnd, StreamID: 5, Payload: []byte{}},
		{Type: FrameReset, StreamID: 7, Payload: []byte{byte(ResetIdle)}},
		{Type: FrameWindow, StreamID: 0xffffffff, Payload: creditPayload(4096)},
	}
	for _, want := range cases {
		message := EncodeFrame(want)
		if !bytes.Equal(message, rawFrame(byte(want.Type), want.StreamID, want.Payload)) {
			t.Fatalf("EncodeFrame(%v) = %x", want, message)
		}
		got, err := DecodeFrame(message)
		if err != nil {
			t.Fatalf("DecodeFrame(%x): %v", message, err)
		}
		if got.Type != want.Type || got.StreamID != want.StreamID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("DecodeFrame(%x) = %+v, want %+v", message, got, want)
		}
	}
}

func TestFrameDecodeRejectsProtocolErrors(t *testing.T) {
	cases := []struct {
		name    string
		message []byte
	}{
		{"message_4_bytes", []byte{2, 0, 0, 0}},
		{"message_65542_bytes", rawFrame(2, 1, bytes.Repeat([]byte{'a'}, 65537))},
		{"type_0", rawFrame(0, 1, []byte("a"))},
		{"type_6", rawFrame(6, 1, []byte("a"))},
		{"stream_id_0", rawFrame(2, 0, []byte("a"))},
		{"stream_id_2", rawFrame(3, 2, nil)},
		{"data_even_stream_id", rawFrame(2, 4, []byte("a"))},
		{"data_empty", rawFrame(2, 1, nil)},
		{"end_1_byte", rawFrame(3, 1, []byte{0})},
		{"reset_0_bytes", rawFrame(4, 1, nil)},
		{"reset_2_bytes", rawFrame(4, 1, []byte{1, 1})},
		{"window_3_bytes", rawFrame(5, 1, []byte{0, 0, 1})},
		{"window_credit_0", rawFrame(5, 1, creditPayload(0))},
		{"window_credit_2_pow_31", rawFrame(5, 1, creditPayload(1<<31))},
		{"open_empty", rawFrame(1, 1, nil)},
		{"open_1025_bytes", rawFrame(1, 1, openPayloadOfSize(1025))},
		{"open_invalid_utf8", rawFrame(1, 1, []byte("{\"preview_id\":\"p\xff\",\"preview_key\":\"k\"}"))},
		{"open_json_array", rawFrame(1, 1, []byte(`[{"preview_id":"p","preview_key":"k"}]`))},
		{"open_duplicate_preview_id", rawFrame(1, 1, []byte(`{"preview_id":"p","preview_id":"q","preview_key":"k"}`))},
		{"open_extra_key", rawFrame(1, 1, []byte(`{"preview_id":"p","preview_key":"k","port":8080}`))},
		{"open_missing_key", rawFrame(1, 1, []byte(`{"preview_id":"p"}`))},
		{"open_empty_value", rawFrame(1, 1, []byte(`{"preview_id":"","preview_key":"k"}`))},
		{"open_numeric_value", rawFrame(1, 1, []byte(`{"preview_id":1,"preview_key":"k"}`))},
		{"open_trailing_data", rawFrame(1, 1, []byte(`{"preview_id":"p","preview_key":"k"}{}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeFrame(tc.message); !errors.Is(err, ErrProtocol) {
				t.Fatalf("DecodeFrame(%x) error = %v, want ErrProtocol", tc.message, err)
			}
		})
	}
}

func TestFrameDecodeAcceptsBoundaries(t *testing.T) {
	maxData := rawFrame(2, 1, bytes.Repeat([]byte{'a'}, MaxPayload))
	if len(maxData) != MaxMessage || MaxMessage != 65541 {
		t.Fatalf("max DATA message is %d bytes", len(maxData))
	}
	cases := []struct {
		name    string
		message []byte
	}{
		{"data_65541_byte_message", maxData},
		{"data_1_byte", rawFrame(2, 1, []byte{0})},
		{"window_credit_max", rawFrame(5, 1, creditPayload(MaxWindow))},
		{"window_credit_1", rawFrame(5, 1, creditPayload(1))},
		{"open_1024_bytes", rawFrame(1, 1, openPayloadOfSize(1024))},
		{"reset_code_7", rawFrame(4, 1, []byte{7})},
		{"reset_code_0", rawFrame(4, 1, []byte{0})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeFrame(tc.message); err != nil {
				t.Fatalf("DecodeFrame: %v", err)
			}
		})
	}
}

func TestFrameWindowCredit(t *testing.T) {
	frame, err := DecodeFrame(rawFrame(5, 1, creditPayload(MaxWindow)))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := WindowCredit(frame); !ok || got != MaxWindow {
		t.Fatalf("WindowCredit = %d, %v", got, ok)
	}
	for _, frame := range []Frame{
		{Type: FrameData, StreamID: 1, Payload: creditPayload(1)},
		{Type: FrameWindow, StreamID: 1, Payload: []byte{0, 0, 1}},
		{Type: FrameWindow, StreamID: 1, Payload: []byte{0, 0, 0, 0, 1}},
	} {
		if got, ok := WindowCredit(frame); ok || got != 0 {
			t.Fatalf("WindowCredit(%+v) = %d, %v", frame, got, ok)
		}
	}
}

func TestFramePayloadAliasesMessage(t *testing.T) {
	message := rawFrame(2, 1, []byte("abc"))
	frame, err := DecodeFrame(message)
	if err != nil {
		t.Fatal(err)
	}
	message[5] = 'z'
	if frame.Payload[0] != 'z' {
		t.Fatalf("Payload = %q, want it to alias the message", frame.Payload)
	}
}

func TestFrameResetCodeOf(t *testing.T) {
	cases := map[byte]ResetCode{
		0:   ResetProtocol,
		1:   ResetNotFound,
		2:   ResetRefused,
		3:   ResetLimit,
		4:   ResetUpstreamError,
		5:   ResetIdle,
		6:   ResetProtocol,
		7:   ResetProtocol,
		255: ResetProtocol,
	}
	for code, want := range cases {
		frame, err := DecodeFrame(rawFrame(4, 1, []byte{code}))
		if err != nil {
			t.Fatalf("code %d: %v", code, err)
		}
		got, raw, ok := ResetCodeOf(frame)
		if !ok || got != want || raw != code {
			t.Fatalf("ResetCodeOf(code %d) = %d, %d, %v, want %d", code, got, raw, ok, want)
		}
	}
	for _, frame := range []Frame{
		{Type: FrameData, StreamID: 1, Payload: []byte{1}},
		{Type: FrameReset, StreamID: 1, Payload: nil},
		{Type: FrameReset, StreamID: 1, Payload: []byte{1, 1}},
	} {
		if got, raw, ok := ResetCodeOf(frame); ok || got != 0 || raw != 0 {
			t.Fatalf("ResetCodeOf(%+v) = %d, %d, %v", frame, got, raw, ok)
		}
	}
}

func TestFrameDecodeOpenSurrogateEscapes(t *testing.T) {
	cases := map[string]bool{
		jsonUnit("d83d") + jsonUnit("de00"):                    true,
		jsonUnit("D83D") + jsonUnit("DE00"):                    true,
		jsonUnit("0041"):                                       true,
		`a\\ud800`:                                             true,
		jsonUnit("d800"):                                       false,
		jsonUnit("dbff"):                                       false,
		jsonUnit("dc00"):                                       false,
		jsonUnit("dfff"):                                       false,
		jsonUnit("de00") + jsonUnit("d83d"):                    false,
		jsonUnit("d83d") + "x" + jsonUnit("de00"):              false,
		jsonUnit("d83d") + jsonUnit("0041"):                    false,
		jsonUnit("d83d") + jsonUnit("d83d"):                    false,
		"p" + jsonUnit("d800"):                                 false,
		`\\` + jsonUnit("d800"):                                false,
		jsonUnit("d83d") + jsonUnit("de00") + jsonUnit("d800"): false,
	}
	for value, accepted := range cases {
		for _, payload := range []string{
			`{"preview_id":"` + value + `","preview_key":"k"}`,
			`{"preview_id":"p","preview_key":"` + value + `"}`,
		} {
			_, err := DecodeOpen([]byte(payload))
			if accepted && err != nil {
				t.Fatalf("DecodeOpen(%s): %v", payload, err)
			}
			if !accepted && !errors.Is(err, ErrProtocol) {
				t.Fatalf("DecodeOpen(%s) error = %v, want ErrProtocol", payload, err)
			}
		}
	}
	got, err := DecodeOpen([]byte(`{"preview_id":"` + jsonUnit("d83d") + jsonUnit("de00") + `","preview_key":"k"}`))
	if err != nil || got.PreviewID != "\U0001F600" {
		t.Fatalf("DecodeOpen = %+v, %v", got, err)
	}
}

func TestFrameDecodeOpen(t *testing.T) {
	got, err := DecodeOpen([]byte(` {"preview_key":"key_1","preview_id":"prv_1"} `))
	if err != nil {
		t.Fatal(err)
	}
	if got != (OpenRequest{PreviewID: "prv_1", PreviewKey: "key_1"}) {
		t.Fatalf("DecodeOpen = %+v", got)
	}
	if _, err := DecodeOpen([]byte(`{"preview_id":"p","preview_key":"k","preview_key":"k"}`)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("duplicate preview_key error = %v", err)
	}
}

func TestFrameConstants(t *testing.T) {
	if MaxPayload != 65536 || InitialWindow != 262144 || MaxWindow != 2147483647 || TunnelCreditCap != 16777216 || StreamsPerPreview != 64 || StreamsPerTunnel != 256 {
		t.Fatal("constants do not match preview tunnel v1")
	}
	codes := []ResetCode{ResetNotFound, ResetRefused, ResetLimit, ResetUpstreamError, ResetIdle, ResetProtocol}
	for index, code := range codes {
		if int(code) != index+1 {
			t.Fatalf("reset code %d = %d", index, code)
		}
	}
	types := []FrameType{FrameOpen, FrameData, FrameEnd, FrameReset, FrameWindow}
	for index, frameType := range types {
		if int(frameType) != index+1 {
			t.Fatalf("frame type %d = %d", index, frameType)
		}
	}
}
