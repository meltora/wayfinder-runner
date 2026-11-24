package previewtunnel

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

const (
	previewTunnelVectorsPath      = "../../api/runner/v1/testdata/preview_tunnel_v1_frames.json"
	previewTunnelVectorsUpdateEnv = "PREVIEW_TUNNEL_VECTORS_UPDATE"
	expectOK                      = "ok"
	expectClose1002               = "close_1002"
)

type PreviewTunnelVectorFile struct {
	SchemaVersion int                     `json:"schema_version"`
	Generator     string                  `json:"generator"`
	Conventions   map[string]string       `json:"conventions"`
	Frames        []PreviewTunnelFrame    `json:"frames"`
	Sequences     []PreviewTunnelSequence `json:"sequences"`
}

type PreviewTunnelFrame struct {
	Name    string                     `json:"name"`
	Hex     string                     `json:"hex"`
	Expect  string                     `json:"expect"`
	Decoded *PreviewTunnelFrameDecoded `json:"decoded,omitempty"`
}

type PreviewTunnelFrameDecoded struct {
	Type         FrameType          `json:"type"`
	StreamID     uint32             `json:"stream_id"`
	PayloadHex   string             `json:"payload_hex"`
	Open         *PreviewTunnelOpen `json:"open,omitempty"`
	WindowCredit uint32             `json:"window_credit,omitempty"`
	ResetCode    string             `json:"reset_code,omitempty"`
}

type PreviewTunnelOpen struct {
	PreviewID  string `json:"preview_id"`
	PreviewKey string `json:"preview_key"`
}

type PreviewTunnelSequence struct {
	Name          string                      `json:"name"`
	KnownPreviews []PreviewTunnelOpen         `json:"known_previews"`
	Steps         []PreviewTunnelSequenceStep `json:"steps"`
}

type PreviewTunnelSequenceStep struct {
	From       string   `json:"from"`
	Hex        string   `json:"hex"`
	Expect     string   `json:"expect"`
	RepliesHex []string `json:"replies_hex"`
}

var resetCodeNames = map[ResetCode]string{
	ResetNotFound:      "not_found",
	ResetRefused:       "refused",
	ResetLimit:         "limit",
	ResetUpstreamError: "upstream_error",
	ResetIdle:          "idle",
	ResetProtocol:      "protocol",
}

func okFrame(name string, frameType FrameType, streamID uint32, payload []byte) PreviewTunnelFrame {
	return PreviewTunnelFrame{
		Name:   name,
		Hex:    hex.EncodeToString(rawFrame(byte(frameType), streamID, payload)),
		Expect: expectOK,
		Decoded: &PreviewTunnelFrameDecoded{
			Type:       frameType,
			StreamID:   streamID,
			PayloadHex: hex.EncodeToString(payload),
		},
	}
}

func okOpen(name string, streamID uint32, payload []byte, previewID, previewKey string) PreviewTunnelFrame {
	vector := okFrame(name, FrameOpen, streamID, payload)
	vector.Decoded.Open = &PreviewTunnelOpen{PreviewID: previewID, PreviewKey: previewKey}
	return vector
}

func okWindow(name string, credit uint32) PreviewTunnelFrame {
	vector := okFrame(name, FrameWindow, 1, creditPayload(credit))
	vector.Decoded.WindowCredit = credit
	return vector
}

func okReset(name string, code byte, want ResetCode) PreviewTunnelFrame {
	vector := okFrame(name, FrameReset, 1, []byte{code})
	vector.Decoded.ResetCode = resetCodeNames[want]
	return vector
}

func badFrame(name string, message []byte) PreviewTunnelFrame {
	return PreviewTunnelFrame{Name: name, Hex: hex.EncodeToString(message), Expect: expectClose1002}
}

func badOpen(name string, payload string) PreviewTunnelFrame {
	return badFrame(name, rawFrame(byte(FrameOpen), 1, []byte(payload)))
}

func BuildPreviewTunnelVectorFile() PreviewTunnelVectorFile {
	open1024 := openPayloadOfSize(1024)
	return PreviewTunnelVectorFile{
		SchemaVersion: 1,
		Generator:     previewTunnelVectorsUpdateEnv + "=1 go test ./internal/previewtunnel -run TestPreviewTunnelV1VectorsAreCurrent",
		Conventions: map[string]string{
			"hex":            "one complete websocket binary message, lowercase hex: 1 byte type, 4 bytes big-endian stream id, then the payload",
			"expect":         "ok: the frame passes every stateless check of preview tunnel v1 T2; close_1002: the receiver closes the tunnel with close code 1002",
			"decoded":        "present only when expect is ok; type is the numeric frame type, payload_hex the payload bytes in lowercase hex",
			"open":           "present for OPEN: the strictly parsed preview_id and preview_key",
			"window_credit":  "present for WINDOW: the uint32 big-endian credit",
			"reset_code":     "present for RESET: the code name the receiver acts on; an unknown code decodes ok and maps to protocol",
			"frames":         "stateless decoding only; stream ordering, windows and limits are covered by sequences",
			"sequences":      "stateful replays against a runner that knows known_previews; from gateway is a message the runner receives, replies_hex the messages the runner sends in order",
			"stream_id_rule": "every frame needs an odd, non-zero stream id; even ids are a protocol error in both directions",
		},
		Frames: []PreviewTunnelFrame{
			okOpen("open_ok", 1, []byte(`{"preview_id":"prv_1","preview_key":"key_1"}`), "prv_1", "key_1"),
			okOpen("open_keys_reordered_with_whitespace", 3, []byte(` {"preview_key":"key_1", "preview_id":"prv_1"} `), "prv_1", "key_1"),
			okOpen("open_1024_bytes", 5, open1024, "p", string(open1024[len(`{"preview_id":"p","preview_key":"`):len(open1024)-2])),
			okFrame("data_1_byte", FrameData, 1, []byte{0}),
			okFrame("data_65541_byte_message", FrameData, 1, bytes.Repeat([]byte{'a'}, MaxPayload)),
			okFrame("data_max_stream_id", FrameData, 0xffffffff, []byte("x")),
			okFrame("end_ok", FrameEnd, 1, []byte{}),
			okReset("reset_not_found", 1, ResetNotFound),
			okReset("reset_refused", 2, ResetRefused),
			okReset("reset_limit", 3, ResetLimit),
			okReset("reset_upstream_error", 4, ResetUpstreamError),
			okReset("reset_idle", 5, ResetIdle),
			okReset("reset_protocol", 6, ResetProtocol),
			okReset("reset_code_0_maps_to_protocol", 0, ResetProtocol),
			okReset("reset_code_7_maps_to_protocol", 7, ResetProtocol),
			okWindow("window_credit_1", 1),
			okWindow("window_credit_max", MaxWindow),
			badFrame("message_4_bytes", []byte{2, 0, 0, 0}),
			badFrame("message_empty", []byte{}),
			badFrame("message_65542_bytes", rawFrame(byte(FrameData), 1, bytes.Repeat([]byte{'a'}, MaxPayload+1))),
			badFrame("type_0", rawFrame(0, 1, []byte("a"))),
			badFrame("type_6", rawFrame(6, 1, []byte("a"))),
			badFrame("stream_id_0", rawFrame(byte(FrameData), 0, []byte("a"))),
			badFrame("stream_id_2", rawFrame(byte(FrameEnd), 2, nil)),
			badFrame("data_even_stream_id", rawFrame(byte(FrameData), 4, []byte("a"))),
			badFrame("data_empty", rawFrame(byte(FrameData), 1, nil)),
			badFrame("end_1_byte", rawFrame(byte(FrameEnd), 1, []byte{0})),
			badFrame("reset_0_bytes", rawFrame(byte(FrameReset), 1, nil)),
			badFrame("reset_2_bytes", rawFrame(byte(FrameReset), 1, []byte{1, 1})),
			badFrame("window_3_bytes", rawFrame(byte(FrameWindow), 1, []byte{0, 0, 1})),
			badFrame("window_5_bytes", rawFrame(byte(FrameWindow), 1, []byte{0, 0, 0, 0, 1})),
			badFrame("window_credit_0", rawFrame(byte(FrameWindow), 1, creditPayload(0))),
			badFrame("window_credit_2_pow_31", rawFrame(byte(FrameWindow), 1, creditPayload(1<<31))),
			badOpen("open_empty", ""),
			badFrame("open_1025_bytes", rawFrame(byte(FrameOpen), 1, openPayloadOfSize(1025))),
			badOpen("open_invalid_utf8", "{\"preview_id\":\"p\xff\",\"preview_key\":\"k\"}"),
			badOpen("open_json_array", `[{"preview_id":"p","preview_key":"k"}]`),
			badOpen("open_duplicate_preview_id", `{"preview_id":"p","preview_id":"q","preview_key":"k"}`),
			badOpen("open_duplicate_preview_key", `{"preview_id":"p","preview_key":"k","preview_key":"k"}`),
			badOpen("open_extra_key", `{"preview_id":"p","preview_key":"k","port":8080}`),
			badOpen("open_missing_key", `{"preview_id":"p"}`),
			badOpen("open_empty_value", `{"preview_id":"","preview_key":"k"}`),
			badOpen("open_numeric_value", `{"preview_id":1,"preview_key":"k"}`),
			badOpen("open_null_value", `{"preview_id":"p","preview_key":null}`),
			badOpen("open_trailing_data", `{"preview_id":"p","preview_key":"k"}{}`),
			badFrame("open_even_stream_id", rawFrame(byte(FrameOpen), 2, []byte(`{"preview_id":"p","preview_key":"k"}`))),
		},
		Sequences: []PreviewTunnelSequence{},
	}
}

func encodePreviewTunnelVectorFile(t *testing.T, file PreviewTunnelVectorFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPreviewTunnelV1VectorsAreCurrent(t *testing.T) {
	want := encodePreviewTunnelVectorFile(t, BuildPreviewTunnelVectorFile())
	if os.Getenv(previewTunnelVectorsUpdateEnv) == "1" {
		if err := os.WriteFile(previewTunnelVectorsPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(previewTunnelVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with %s=1", previewTunnelVectorsPath, previewTunnelVectorsUpdateEnv)
	}
}

func TestPreviewTunnelV1VectorsAreDeterministic(t *testing.T) {
	first := encodePreviewTunnelVectorFile(t, BuildPreviewTunnelVectorFile())
	second := encodePreviewTunnelVectorFile(t, BuildPreviewTunnelVectorFile())
	if !bytes.Equal(first, second) {
		t.Fatal("vector generation is not deterministic")
	}
}

func loadPreviewTunnelVectorFile(t *testing.T) PreviewTunnelVectorFile {
	t.Helper()
	raw, err := os.ReadFile(previewTunnelVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var file PreviewTunnelVectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPreviewTunnelV1FrameVectorsDecode(t *testing.T) {
	file := loadPreviewTunnelVectorFile(t)
	names := map[string]bool{}
	for _, vector := range file.Frames {
		if names[vector.Name] {
			t.Fatalf("duplicate vector name %q", vector.Name)
		}
		names[vector.Name] = true
		t.Run(vector.Name, func(t *testing.T) {
			message, err := hex.DecodeString(vector.Hex)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := DecodeFrame(message)
			switch vector.Expect {
			case expectClose1002:
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("DecodeFrame error = %v, want ErrProtocol", err)
				}
				if vector.Decoded != nil {
					t.Fatal("a close_1002 vector has decoded fields")
				}
			case expectOK:
				if err != nil {
					t.Fatalf("DecodeFrame: %v", err)
				}
				assertDecodedVector(t, frame, vector.Decoded)
			default:
				t.Fatalf("unknown expect %q", vector.Expect)
			}
		})
	}
	for _, required := range []string{"data_even_stream_id", "open_duplicate_preview_id", "reset_code_7_maps_to_protocol"} {
		if !names[required] {
			t.Fatalf("missing required vector %q", required)
		}
	}
}

func assertDecodedVector(t *testing.T, frame Frame, want *PreviewTunnelFrameDecoded) {
	t.Helper()
	if want == nil {
		t.Fatal("an ok vector has no decoded fields")
	}
	if frame.Type != want.Type || frame.StreamID != want.StreamID || hex.EncodeToString(frame.Payload) != want.PayloadHex {
		t.Fatalf("decoded %+v, want %+v", frame, want)
	}
	if (frame.Type == FrameOpen) != (want.Open != nil) {
		t.Fatalf("open fields present = %v for type %d", want.Open != nil, frame.Type)
	}
	if want.Open != nil {
		request, err := DecodeOpen(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if request.PreviewID != want.Open.PreviewID || request.PreviewKey != want.Open.PreviewKey {
			t.Fatalf("DecodeOpen = %+v, want %+v", request, want.Open)
		}
	}
	if frame.Type == FrameWindow && WindowCredit(frame) != want.WindowCredit {
		t.Fatalf("WindowCredit = %d, want %d", WindowCredit(frame), want.WindowCredit)
	}
	if frame.Type != FrameWindow && want.WindowCredit != 0 {
		t.Fatalf("window_credit on type %d", frame.Type)
	}
	if frame.Type == FrameReset && resetCodeNames[ResetCodeOf(frame)] != want.ResetCode {
		t.Fatalf("ResetCodeOf = %q, want %q", resetCodeNames[ResetCodeOf(frame)], want.ResetCode)
	}
	if frame.Type != FrameReset && want.ResetCode != "" {
		t.Fatalf("reset_code on type %d", frame.Type)
	}
}
