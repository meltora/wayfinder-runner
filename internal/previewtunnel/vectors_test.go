package previewtunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	previewTunnelVectorsPath      = "../../api/runner/v1/testdata/preview_tunnel_v1_frames.json"
	previewTunnelVectorsUpdateEnv = "PREVIEW_TUNNEL_VECTORS_UPDATE"
	expectOK                      = "ok"
	expectClose1002               = "close_1002"
	maxSpelledRun                 = 1024
)

type PreviewTunnelVectorFile struct {
	SchemaVersion int                     `json:"schema_version"`
	Generator     string                  `json:"generator"`
	Conventions   map[string]string       `json:"conventions"`
	Frames        []PreviewTunnelFrame    `json:"frames"`
	Sequences     []PreviewTunnelSequence `json:"sequences"`
}

type PreviewTunnelFrame struct {
	Name      string                     `json:"name"`
	Hex       string                     `json:"hex"`
	FillByte  string                     `json:"fill_byte,omitempty"`
	FillCount int                        `json:"fill_count,omitempty"`
	Length    int                        `json:"length"`
	Expect    string                     `json:"expect"`
	Decoded   *PreviewTunnelFrameDecoded `json:"decoded,omitempty"`
}

type PreviewTunnelFrameDecoded struct {
	Type             FrameType          `json:"type"`
	StreamID         uint32             `json:"stream_id"`
	PayloadHex       string             `json:"payload_hex"`
	PayloadFillByte  string             `json:"payload_fill_byte,omitempty"`
	PayloadFillCount int                `json:"payload_fill_count,omitempty"`
	Open             *PreviewTunnelOpen `json:"open,omitempty"`
	WindowCredit     uint32             `json:"window_credit,omitempty"`
	ResetCode        string             `json:"reset_code,omitempty"`
}

type PreviewTunnelOpen struct {
	PreviewID  string `json:"preview_id"`
	PreviewKey string `json:"preview_key"`
}

type PreviewTunnelSequence struct {
	Name          string                      `json:"name"`
	KnownPreviews []PreviewTunnelOpen         `json:"known_previews"`
	Upstream      string                      `json:"upstream"`
	Steps         []PreviewTunnelSequenceStep `json:"steps"`
}

type PreviewTunnelSequenceStep struct {
	From                string   `json:"from"`
	Hex                 string   `json:"hex"`
	FillByte            string   `json:"fill_byte,omitempty"`
	FillCount           int      `json:"fill_count,omitempty"`
	Length              int      `json:"length"`
	Expect              string   `json:"expect"`
	RepliesHex          []string `json:"replies_hex"`
	UpstreamReceivesHex *string  `json:"upstream_receives_hex,omitempty"`
	UpstreamEOF         *bool    `json:"upstream_eof,omitempty"`
}

func expectUpstream(step PreviewTunnelSequenceStep, received string) PreviewTunnelSequenceStep {
	receivedHex := hex.EncodeToString([]byte(received))
	step.UpstreamReceivesHex = &receivedHex
	return step
}

func expectUpstreamEOF(step PreviewTunnelSequenceStep, received string, eof bool) PreviewTunnelSequenceStep {
	step = expectUpstream(step, received)
	step.UpstreamEOF = &eof
	return step
}

const (
	expectDrop          = "drop"
	fromGateway         = "gateway"
	upstreamDiscard     = "discard"
	upstreamCloseOnEOF  = "close_on_eof"
	upstreamStall       = "stall"
	sequencePreviewID   = "prv_1"
	sequencePreviewKey  = "key_1"
	sequenceOtherID     = "prv_2"
	sequenceOtherKey    = "key_2"
	sequenceUnknownID   = "prv_9"
	sequenceWindowCover = MaxWindow - InitialWindow
)

func sequenceStep(expect string, message []byte, replies ...[]byte) PreviewTunnelSequenceStep {
	prefix, fillByte, fillCount := compactHex(message)
	repliesHex := make([]string, 0, len(replies))
	for _, reply := range replies {
		repliesHex = append(repliesHex, hex.EncodeToString(reply))
	}
	return PreviewTunnelSequenceStep{From: fromGateway, Hex: prefix, FillByte: fillByte, FillCount: fillCount, Length: len(message), Expect: expect, RepliesHex: repliesHex}
}

func openMessage(streamID uint32, previewID, previewKey string) []byte {
	return rawFrame(byte(FrameOpen), streamID, []byte(`{"preview_id":"`+previewID+`","preview_key":"`+previewKey+`"}`))
}

func openKnown(streamID uint32) []byte {
	return openMessage(streamID, sequencePreviewID, sequencePreviewKey)
}

func dataMessage(streamID uint32, payload []byte) []byte {
	return rawFrame(byte(FrameData), streamID, payload)
}

func endMessage(streamID uint32) []byte {
	return rawFrame(byte(FrameEnd), streamID, nil)
}

func resetMessage(streamID uint32, code byte) []byte {
	return rawFrame(byte(FrameReset), streamID, []byte{code})
}

func windowMessage(streamID uint32, credit uint32) []byte {
	return rawFrame(byte(FrameWindow), streamID, creditPayload(credit))
}

func sequence(name, upstream string, steps ...PreviewTunnelSequenceStep) PreviewTunnelSequence {
	return PreviewTunnelSequence{
		Name:          name,
		KnownPreviews: []PreviewTunnelOpen{{PreviewID: sequencePreviewID, PreviewKey: sequencePreviewKey}, {PreviewID: sequenceOtherID, PreviewKey: sequenceOtherKey}},
		Upstream:      upstream,
		Steps:         steps,
	}
}

func previewLimitSequence() PreviewTunnelSequence {
	steps := make([]PreviewTunnelSequenceStep, 0, StreamsPerPreview+2)
	id := uint32(1)
	for range StreamsPerPreview {
		steps = append(steps, sequenceStep(expectOK, openKnown(id)))
		id += 2
	}
	steps = append(steps, sequenceStep(expectOK, openKnown(id), resetMessage(id, byte(ResetLimit))))
	steps = append(steps, sequenceStep(expectOK, openMessage(id+2, sequenceOtherID, sequenceOtherKey)))
	return sequence("preview_stream_limit_resets_limit", upstreamStall, steps...)
}

func buildSequences() []PreviewTunnelSequence {
	full := bytes.Repeat([]byte{'a'}, MaxPayload)
	return []PreviewTunnelSequence{
		sequence("open_data_end_half_closes", upstreamCloseOnEOF,
			sequenceStep(expectOK, openKnown(1)),
			expectUpstreamEOF(sequenceStep(expectOK, dataMessage(1, []byte("hello"))), "hello", false),
			expectUpstreamEOF(sequenceStep(expectOK, endMessage(1), endMessage(1)), "hello", true),
			expectUpstream(sequenceStep(expectDrop, dataMessage(1, []byte("late"))), "hello"),
			sequenceStep(expectDrop, endMessage(1)),
		),
		sequence("unknown_preview_resets_not_found", upstreamDiscard,
			sequenceStep(expectOK, openMessage(1, sequenceUnknownID, sequencePreviewKey), resetMessage(1, byte(ResetNotFound))),
			sequenceStep(expectOK, openMessage(3, sequencePreviewID, sequenceOtherKey), resetMessage(3, byte(ResetNotFound))),
			sequenceStep(expectDrop, dataMessage(1, []byte("x"))),
			sequenceStep(expectOK, openKnown(5)),
		),
		sequence("data_after_gateway_end_resets_protocol", upstreamDiscard,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, endMessage(1)),
			sequenceStep(expectOK, dataMessage(1, []byte("x")), resetMessage(1, byte(ResetProtocol))),
			sequenceStep(expectOK, openKnown(3)),
			sequenceStep(expectOK, dataMessage(3, []byte("y"))),
		),
		sequence("second_end_closes_1002", upstreamDiscard,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, endMessage(1)),
			sequenceStep(expectClose1002, endMessage(1)),
		),
		sequence("open_same_id_closes_1002", upstreamDiscard,
			sequenceStep(expectOK, openKnown(3)),
			sequenceStep(expectClose1002, openKnown(3)),
		),
		sequence("open_lower_id_closes_1002", upstreamDiscard,
			sequenceStep(expectOK, openKnown(3)),
			sequenceStep(expectClose1002, openKnown(1)),
		),
		sequence("frame_above_highest_opened_closes_1002", upstreamDiscard,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectClose1002, dataMessage(3, []byte("x"))),
		),
		sequence("frame_before_any_open_closes_1002", upstreamDiscard,
			sequenceStep(expectClose1002, windowMessage(1, 1)),
		),
		sequence("closed_stream_frames_dropped", upstreamDiscard,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, resetMessage(1, byte(ResetRefused))),
			expectUpstream(sequenceStep(expectDrop, dataMessage(1, []byte("x"))), ""),
			sequenceStep(expectDrop, endMessage(1)),
			sequenceStep(expectDrop, endMessage(1)),
			sequenceStep(expectDrop, windowMessage(1, MaxWindow)),
			sequenceStep(expectDrop, resetMessage(1, byte(ResetIdle))),
			sequenceStep(expectOK, openKnown(5)),
			sequenceStep(expectDrop, dataMessage(3, []byte("x"))),
			expectUpstreamEOF(sequenceStep(expectOK, dataMessage(5, []byte("y"))), "y", false),
		),
		sequence("reset_code_7_closes_stream_only", upstreamDiscard,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, resetMessage(1, 7)),
			sequenceStep(expectDrop, dataMessage(1, []byte("x"))),
			sequenceStep(expectOK, openKnown(3)),
			sequenceStep(expectOK, dataMessage(3, []byte("y"))),
		),
		sequence("data_beyond_window_resets_protocol", upstreamStall,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, dataMessage(1, full)),
			sequenceStep(expectOK, dataMessage(1, full)),
			sequenceStep(expectOK, dataMessage(1, full)),
			sequenceStep(expectOK, dataMessage(1, full)),
			sequenceStep(expectOK, dataMessage(1, []byte("b")), resetMessage(1, byte(ResetProtocol))),
			sequenceStep(expectOK, openKnown(3)),
		),
		sequence("window_granted_after_upstream_consumes", upstreamDiscard,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, dataMessage(1, full)),
			sequenceStep(expectOK, dataMessage(1, full), windowMessage(1, 2*MaxPayload)),
		),
		sequence("window_grant_overflow_closes_1002", upstreamStall,
			sequenceStep(expectOK, openKnown(1)),
			sequenceStep(expectOK, windowMessage(1, sequenceWindowCover)),
			sequenceStep(expectClose1002, windowMessage(1, 1)),
		),
		previewLimitSequence(),
	}
}

var resetCodeNames = map[ResetCode]string{
	ResetNotFound:      "not_found",
	ResetRefused:       "refused",
	ResetLimit:         "limit",
	ResetUpstreamError: "upstream_error",
	ResetIdle:          "idle",
	ResetProtocol:      "protocol",
}

func compactHex(data []byte) (string, string, int) {
	run := 0
	for run < len(data) && data[len(data)-1-run] == data[len(data)-1] {
		run++
	}
	if run <= maxSpelledRun {
		return hex.EncodeToString(data), "", 0
	}
	return hex.EncodeToString(data[:len(data)-run]), hex.EncodeToString(data[len(data)-1:]), run
}

func expandHex(prefix, fillByte string, fillCount int) ([]byte, error) {
	if prefix != strings.ToLower(prefix) {
		return nil, fmt.Errorf("hex %q is not lowercase", prefix)
	}
	data, err := hex.DecodeString(prefix)
	if err != nil {
		return nil, err
	}
	if fillByte == "" && fillCount == 0 {
		return data, nil
	}
	if len(fillByte) != 2 || fillByte != strings.ToLower(fillByte) || fillCount <= 0 {
		return nil, fmt.Errorf("fill byte %q with count %d is invalid", fillByte, fillCount)
	}
	fill, err := hex.DecodeString(fillByte)
	if err != nil {
		return nil, err
	}
	return append(data, bytes.Repeat(fill, fillCount)...), nil
}

func messageVector(name string, message []byte, expect string) PreviewTunnelFrame {
	prefix, fillByte, fillCount := compactHex(message)
	return PreviewTunnelFrame{Name: name, Hex: prefix, FillByte: fillByte, FillCount: fillCount, Length: len(message), Expect: expect}
}

func okFrame(name string, frameType FrameType, streamID uint32, payload []byte) PreviewTunnelFrame {
	vector := messageVector(name, rawFrame(byte(frameType), streamID, payload), expectOK)
	payloadHex, payloadFillByte, payloadFillCount := compactHex(payload)
	vector.Decoded = &PreviewTunnelFrameDecoded{
		Type:             frameType,
		StreamID:         streamID,
		PayloadHex:       payloadHex,
		PayloadFillByte:  payloadFillByte,
		PayloadFillCount: payloadFillCount,
	}
	return vector
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
	return messageVector(name, message, expectClose1002)
}

func badOpen(name string, payload string) PreviewTunnelFrame {
	return badFrame(name, rawFrame(byte(FrameOpen), 1, []byte(payload)))
}

func openWith(previewID string) string {
	return `{"preview_id":"` + previewID + `","preview_key":"k"}`
}

func multibyteOpenPayloadOfSize(size int) string {
	base := `{"preview_id":"p","preview_key":""}`
	return `{"preview_id":"p","preview_key":"` + strings.Repeat("é", (size-len(base))/2) + `"}`
}

var divergenceVectorNames = []string{
	"open_utf8_bom_before_object",
	"open_1025_bytes_multibyte",
	"open_utf8_encoded_surrogate",
	"open_utf8_overlong",
	"open_duplicate_key_via_escape",
	"open_extra_key_proto",
	"open_extra_key_constructor",
	"open_formfeed_before_object",
	"open_nbsp_before_object",
	"open_u2028_before_object",
	"open_object_value",
	"open_array_value",
	"open_true_value",
	"open_trailing_comma",
	"open_missing_comma",
	"open_lone_high_surrogate_escape",
	"open_lone_low_surrogate_escape",
	"open_reversed_surrogate_escapes",
	"open_escaped_key",
	"open_tab_cr_lf_whitespace",
	"open_surrogate_pair_escape",
}

func BuildPreviewTunnelVectorFile() PreviewTunnelVectorFile {
	open1024 := openPayloadOfSize(1024)
	surrogatePair := jsonUnit("d83d") + jsonUnit("de00")
	return PreviewTunnelVectorFile{
		SchemaVersion: 1,
		Generator:     previewTunnelVectorsUpdateEnv + "=1 go test ./internal/previewtunnel -run TestPreviewTunnelV1VectorsAreCurrent",
		Conventions: map[string]string{
			"hex":               "one complete websocket binary message, lowercase hex: 1 byte type, 4 bytes big-endian stream id, then the payload",
			"fill":              "optional fill_byte (one lowercase hex byte) and fill_count: the message is hex followed by fill_byte repeated fill_count times; decoded.payload_fill_byte and decoded.payload_fill_count apply the same rule to payload_hex; fill is used only when the repeated run is over 1024 bytes",
			"length":            "required: the total message length in bytes after fill expansion; replayers must assert it",
			"expect":            "ok: the frame passes every stateless check of preview tunnel v1 T2; close_1002: the receiver closes the tunnel with close code 1002",
			"receiver":          "frames are judged as received by the runner; a gateway receiving OPEN closes 1002 regardless of payload",
			"decoded":           "present only when expect is ok; type is the numeric frame type, payload_hex the payload bytes in lowercase hex",
			"open":              "present for OPEN: the strictly parsed preview_id and preview_key",
			"window_credit":     "present for WINDOW: the uint32 big-endian credit",
			"reset_code":        "present for RESET: the code name the receiver acts on; an unknown code decodes ok and maps to protocol",
			"frames":            "stateless decoding only; stream ordering, windows and limits are covered by sequences",
			"sequences":         "stateful replays from the runner's point of view; each sequence starts on a fresh tunnel; known_previews are the hosted previews the runner owns, all deployed and unexpired; an OPEN naming any other preview_id, or a known preview_id with a different preview_key, is answered with RESET not_found and nothing is dialed; steps run in order",
			"sequence_step":     "from is always gateway: hex, expanded with fill_byte and fill_count exactly as for frames and checked against length, is one websocket binary message the runner receives",
			"sequence_expect":   "ok: the runner accepts the message and the tunnel stays open; drop: the message names an odd stream id at or below the highest opened id that is not open (closed, reset, or skipped by the gateway), it is ignored without any reply or state change; close_1002: the runner closes the tunnel with close code 1002 and sends nothing more; close_1002 is always the last step; a second END is drop once both ENDs have closed the stream and close_1002 while the stream is still open",
			"sequence_replies":  "replies_hex is the complete, ordered list of messages the runner sends because of that step, including replies that depend on the upstream such as RESET after a failed lookup, END after the upstream closes, or WINDOW after bytes reach the upstream; the runner sends nothing else; a replayer waits for exactly these replies, then waits for 50 ms of silence before delivering the next step; the 50 ms is advisory for replay only and is not a protocol timing rule; when a sequence ends without close_1002 the gateway closes the tunnel",
			"upstream_checks":   "optional per step: upstream_receives_hex is the complete byte string, in lowercase hex, that the upstream of the step's stream id must have received from the runner since that stream was opened, by the end of the step (an empty string means nothing); upstream_eof true means that upstream has read the runner's half-close by then, false means it has not; each is absent when not asserted, and neither appears in stall sequences",
			"sequence_upstream": "upstream is how the local upstream behind every known preview behaves: discard reads and discards all bytes and never writes or closes, ignoring the runner's half-close; close_on_eof reads and discards and closes its side once it reads EOF, so the runner sends END; stall accepts the connection and never reads, writes or closes, so gateway DATA stays queued and unconsumed",
			"window_policy":     "runner policy, not protocol: credit is granted only after bytes are written to the upstream, as one WINDOW per stream once at least 131072 written bytes are ungranted, never while 16777216 or more received bytes are unconsumed across the tunnel, and withheld credit is sent once the total drops below that; a gateway must not rely on grant size or timing beyond the spec",
			"stream_id_rule":    "every frame needs an odd, non-zero stream id; even ids are a protocol error in both directions",
		},
		Frames: []PreviewTunnelFrame{
			okOpen("open_ok", 1, []byte(`{"preview_id":"prv_1","preview_key":"key_1"}`), "prv_1", "key_1"),
			okOpen("open_keys_reordered_with_whitespace", 3, []byte(` {"preview_key":"key_1", "preview_id":"prv_1"} `), "prv_1", "key_1"),
			okOpen("open_1024_bytes", 5, open1024, "p", string(open1024[len(`{"preview_id":"p","preview_key":"`):len(open1024)-2])),
			okOpen("open_escaped_key", 7, []byte(`{"preview_`+jsonUnit("0069")+`d":"p","preview_key":"k"}`), "p", "k"),
			okOpen("open_tab_cr_lf_whitespace", 9, []byte("\t\r\n"+openWith("p")+"\t\r\n"), "p", "k"),
			okOpen("open_surrogate_pair_escape", 11, []byte(openWith(surrogatePair)), "\U0001F600", "k"),
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
			badOpen("open_1025_bytes_multibyte", multibyteOpenPayloadOfSize(1025)),
			badOpen("open_invalid_utf8", "{\"preview_id\":\"p\xff\",\"preview_key\":\"k\"}"),
			badOpen("open_utf8_bom_before_object", "\xef\xbb\xbf"+openWith("p")),
			badOpen("open_utf8_encoded_surrogate", openWith("\xed\xa0\x80")),
			badOpen("open_utf8_overlong", openWith("\xc0\xaf")),
			badOpen("open_json_array", `[{"preview_id":"p","preview_key":"k"}]`),
			badOpen("open_duplicate_preview_id", `{"preview_id":"p","preview_id":"q","preview_key":"k"}`),
			badOpen("open_duplicate_preview_key", `{"preview_id":"p","preview_key":"k","preview_key":"k"}`),
			badOpen("open_duplicate_key_via_escape", `{"preview_id":"p","preview_`+jsonUnit("0069")+`d":"q","preview_key":"k"}`),
			badOpen("open_extra_key", `{"preview_id":"p","preview_key":"k","port":8080}`),
			badOpen("open_extra_key_proto", `{"__proto__":"x","preview_id":"p","preview_key":"k"}`),
			badOpen("open_extra_key_constructor", `{"constructor":"x","preview_id":"p","preview_key":"k"}`),
			badOpen("open_missing_key", `{"preview_id":"p"}`),
			badOpen("open_empty_value", `{"preview_id":"","preview_key":"k"}`),
			badOpen("open_numeric_value", `{"preview_id":1,"preview_key":"k"}`),
			badOpen("open_null_value", `{"preview_id":"p","preview_key":null}`),
			badOpen("open_object_value", `{"preview_id":{"a":"p"},"preview_key":"k"}`),
			badOpen("open_array_value", `{"preview_id":["p"],"preview_key":"k"}`),
			badOpen("open_true_value", `{"preview_id":true,"preview_key":"k"}`),
			badOpen("open_trailing_comma", `{"preview_id":"p","preview_key":"k",}`),
			badOpen("open_missing_comma", `{"preview_id":"p" "preview_key":"k"}`),
			badOpen("open_trailing_data", `{"preview_id":"p","preview_key":"k"}{}`),
			badOpen("open_formfeed_before_object", "\f"+openWith("p")),
			badOpen("open_nbsp_before_object", "\xc2\xa0"+openWith("p")),
			badOpen("open_u2028_before_object", "\xe2\x80\xa8"+openWith("p")),
			badOpen("open_lone_high_surrogate_escape", openWith(jsonUnit("d800"))),
			badOpen("open_lone_low_surrogate_escape", openWith(jsonUnit("dc00"))),
			badOpen("open_reversed_surrogate_escapes", openWith(jsonUnit("de00")+jsonUnit("d83d"))),
			badFrame("open_even_stream_id", rawFrame(byte(FrameOpen), 2, []byte(`{"preview_id":"p","preview_key":"k"}`))),
		},
		Sequences: buildSequences(),
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
			message, err := expandHex(vector.Hex, vector.FillByte, vector.FillCount)
			if err != nil {
				t.Fatal(err)
			}
			if len(message) != vector.Length {
				t.Fatalf("expanded message has %d bytes, want length %d", len(message), vector.Length)
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
	required := append([]string{"data_even_stream_id", "open_duplicate_preview_id", "reset_code_7_maps_to_protocol", "open_1024_bytes", "open_1025_bytes"}, divergenceVectorNames...)
	for _, name := range required {
		if !names[name] {
			t.Fatalf("missing required vector %q", name)
		}
	}
}

func TestPreviewTunnelV1VectorsSpellOutOpenBoundaries(t *testing.T) {
	for _, vector := range loadPreviewTunnelVectorFile(t).Frames {
		if strings.HasPrefix(vector.Name, "open_") && (vector.FillByte != "" || vector.FillCount != 0) {
			t.Fatalf("%s uses fill", vector.Name)
		}
		if vector.Decoded != nil && vector.Decoded.Type == FrameOpen && (vector.Decoded.PayloadFillByte != "" || vector.Decoded.PayloadFillCount != 0) {
			t.Fatalf("%s uses payload fill", vector.Name)
		}
	}
}

func assertDecodedVector(t *testing.T, frame Frame, want *PreviewTunnelFrameDecoded) {
	t.Helper()
	if want == nil {
		t.Fatal("an ok vector has no decoded fields")
	}
	payload, err := expandHex(want.PayloadHex, want.PayloadFillByte, want.PayloadFillCount)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != want.Type || frame.StreamID != want.StreamID || !bytes.Equal(frame.Payload, payload) {
		t.Fatalf("decoded type %d stream %d payload %d bytes, want %+v", frame.Type, frame.StreamID, len(frame.Payload), want)
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
	credit, creditOK := WindowCredit(frame)
	if creditOK != (frame.Type == FrameWindow) || credit != want.WindowCredit {
		t.Fatalf("WindowCredit = %d, %v, want %d", credit, creditOK, want.WindowCredit)
	}
	code, raw, resetOK := ResetCodeOf(frame)
	if resetOK != (frame.Type == FrameReset) {
		t.Fatalf("ResetCodeOf ok = %v for type %d", resetOK, frame.Type)
	}
	if resetOK && (resetCodeNames[code] != want.ResetCode || raw != frame.Payload[0]) {
		t.Fatalf("ResetCodeOf = %q, %d, want %q", resetCodeNames[code], raw, want.ResetCode)
	}
	if !resetOK && want.ResetCode != "" {
		t.Fatalf("reset_code on type %d", frame.Type)
	}
}

var requiredSequenceNames = []string{
	"open_data_end_half_closes",
	"unknown_preview_resets_not_found",
	"data_after_gateway_end_resets_protocol",
	"second_end_closes_1002",
	"open_same_id_closes_1002",
	"open_lower_id_closes_1002",
	"frame_above_highest_opened_closes_1002",
	"frame_before_any_open_closes_1002",
	"closed_stream_frames_dropped",
	"reset_code_7_closes_stream_only",
	"data_beyond_window_resets_protocol",
	"window_granted_after_upstream_consumes",
	"window_grant_overflow_closes_1002",
	"preview_stream_limit_resets_limit",
}

func TestPreviewTunnelV1SequenceVectorsReplay(t *testing.T) {
	file := loadPreviewTunnelVectorFile(t)
	names := map[string]bool{}
	for _, vector := range file.Sequences {
		if names[vector.Name] {
			t.Fatalf("duplicate sequence name %q", vector.Name)
		}
		names[vector.Name] = true
		t.Run(vector.Name, func(t *testing.T) { replaySequence(t, vector) })
	}
	for _, name := range requiredSequenceNames {
		if !names[name] {
			t.Fatalf("missing required sequence %q", name)
		}
	}
}

const sequenceSilence = 50 * time.Millisecond

type upstreamRecorder struct {
	mu       sync.Mutex
	received []byte
	eof      bool
}

func (r *upstreamRecorder) state() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.received), r.eof
}

func driveSequenceUpstream(mode string, upstream *halfConn, recorder *upstreamRecorder) {
	if mode == upstreamStall {
		return
	}
	buffer := make([]byte, MaxPayload)
	for {
		n, err := upstream.Read(buffer)
		recorder.mu.Lock()
		recorder.received = append(recorder.received, buffer[:n]...)
		if errors.Is(err, io.EOF) {
			recorder.eof = true
		}
		recorder.mu.Unlock()
		if err != nil {
			break
		}
	}
	if mode == upstreamCloseOnEOF {
		_ = upstream.Close()
	}
}

func assertSequenceUpstream(t *testing.T, index int, step PreviewTunnelSequenceStep, recorder *upstreamRecorder) {
	t.Helper()
	if step.UpstreamReceivesHex == nil && step.UpstreamEOF == nil {
		return
	}
	if recorder == nil {
		t.Fatalf("step %d: upstream checks on a stream without an upstream", index)
	}
	want := []byte{}
	if step.UpstreamReceivesHex != nil {
		decoded, err := hex.DecodeString(*step.UpstreamReceivesHex)
		if err != nil {
			t.Fatal(err)
		}
		want = decoded
	}
	deadline := time.Now().Add(waitTimeout)
	for {
		received, eof := recorder.state()
		receivedOK := step.UpstreamReceivesHex == nil || bytes.Equal(received, want)
		eofOK := step.UpstreamEOF == nil || eof == *step.UpstreamEOF
		if receivedOK && eofOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("step %d: upstream received %x eof %v, want %x eof %v", index, received, eof, want, step.UpstreamEOF != nil && *step.UpstreamEOF)
		}
		time.Sleep(time.Millisecond)
	}
}

func replaySequence(t *testing.T, vector PreviewTunnelSequence) {
	switch vector.Upstream {
	case upstreamDiscard, upstreamCloseOnEOF, upstreamStall:
	default:
		t.Fatalf("unknown upstream %q", vector.Upstream)
	}
	resolver := &fakeResolver{previews: map[string]fakePreview{}}
	addresses := map[string]bool{}
	for index, preview := range vector.KnownPreviews {
		address := fmt.Sprintf("127.0.0.1:%d", 20080+index)
		resolver.previews[preview.PreviewID] = fakePreview{key: preview.PreviewKey, address: address}
		addresses[address] = true
	}
	dialed := make(chan *upstreamRecorder, 1024)
	dial := func(_ context.Context, address string) (net.Conn, error) {
		if !addresses[address] {
			return nil, errors.New("unknown upstream address")
		}
		local, remote := duplexPipe()
		recorder := &upstreamRecorder{}
		go driveSequenceUpstream(vector.Upstream, remote, recorder)
		dialed <- recorder
		return local, nil
	}
	recorders := map[uint32]*upstreamRecorder{}
	h := &harness{t: t, conn: newFakeConn(), result: make(chan error, 1)}
	h.session = NewSession(h.conn, resolver, dial, nil, nil)
	h.session.idleTicks = make(chan time.Time)
	h.start()
	for index, step := range vector.Steps {
		if step.From != fromGateway {
			t.Fatalf("step %d: from %q", index, step.From)
		}
		message, err := expandHex(step.Hex, step.FillByte, step.FillCount)
		if err != nil {
			t.Fatalf("step %d: %v", index, err)
		}
		if len(message) != step.Length {
			t.Fatalf("step %d: expanded message has %d bytes, want length %d", index, len(message), step.Length)
		}
		switch step.Expect {
		case expectClose1002:
			if index != len(vector.Steps)-1 || len(step.RepliesHex) != 0 {
				t.Fatalf("step %d: close_1002 must be the last step and have no replies", index)
			}
			h.conn.in <- message
			h.expectClosed1002()
			h.expectQuietFor(sequenceSilence)
			return
		case expectDrop:
			if len(step.RepliesHex) != 0 {
				t.Fatalf("step %d: drop with replies", index)
			}
		case expectOK:
		default:
			t.Fatalf("step %d: unknown expect %q", index, step.Expect)
		}
		h.deliverMessage(message)
		for _, replyHex := range step.RepliesHex {
			want, err := hex.DecodeString(replyHex)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-h.conn.out:
				if !bytes.Equal(got, want) {
					t.Fatalf("step %d: runner sent %x, want %x", index, got, want)
				}
			case <-time.After(waitTimeout):
				t.Fatalf("step %d: runner did not send %s", index, replyHex)
			}
		}
		if message[0] == byte(FrameOpen) && step.Expect == expectOK && len(step.RepliesHex) == 0 {
			select {
			case recorder := <-dialed:
				recorders[binary.BigEndian.Uint32(message[1:5])] = recorder
			case <-time.After(waitTimeout):
				t.Fatalf("step %d: accepted OPEN was not dialed", index)
			}
		}
		h.expectQuietFor(sequenceSilence)
		if vector.Upstream == upstreamStall && (step.UpstreamReceivesHex != nil || step.UpstreamEOF != nil) {
			t.Fatalf("step %d: upstream checks in a stall sequence", index)
		}
		assertSequenceUpstream(t, index, step, recorders[binary.BigEndian.Uint32(message[1:5])])
	}
	if len(dialed) != 0 {
		t.Fatal("the runner dialed an upstream for an OPEN it answered")
	}
	close(h.conn.in)
	select {
	case err := <-h.result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Run error = %v, want io.EOF", err)
		}
		h.result <- err
	case <-time.After(waitTimeout):
		t.Fatal("session did not stop")
	}
}
