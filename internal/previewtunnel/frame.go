package previewtunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/example/app-runner/internal/strictjson"
)

type FrameType byte

const (
	FrameOpen   FrameType = 1
	FrameData   FrameType = 2
	FrameEnd    FrameType = 3
	FrameReset  FrameType = 4
	FrameWindow FrameType = 5
)

type ResetCode byte

const (
	ResetNotFound      ResetCode = 1
	ResetRefused       ResetCode = 2
	ResetLimit         ResetCode = 3
	ResetUpstreamError ResetCode = 4
	ResetIdle          ResetCode = 5
	ResetProtocol      ResetCode = 6
)

const (
	MaxPayload        = 65536
	MaxMessage        = frameHeaderSize + MaxPayload
	InitialWindow     = 262144
	MaxWindow         = 1<<31 - 1
	TunnelCreditCap   = 16 << 20
	StreamsPerPreview = 64
	StreamsPerTunnel  = 256

	frameHeaderSize    = 5
	maxOpenPayload     = 1024
	openPreviewIDKey   = "preview_id"
	openPreviewKeyName = "preview_key"
)

var ErrProtocol = errors.New("preview tunnel protocol error")

type Frame struct {
	Type     FrameType
	StreamID uint32
	Payload  []byte
}

type OpenRequest struct {
	PreviewID  string
	PreviewKey string
}

func protocolError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrProtocol, fmt.Sprintf(format, args...))
}

func EncodeFrame(frame Frame) []byte {
	message := make([]byte, frameHeaderSize, frameHeaderSize+len(frame.Payload))
	message[0] = byte(frame.Type)
	binary.BigEndian.PutUint32(message[1:frameHeaderSize], frame.StreamID)
	return append(message, frame.Payload...)
}

func DecodeFrame(message []byte) (Frame, error) {
	if len(message) < frameHeaderSize {
		return Frame{}, protocolError("message of %d bytes is shorter than the frame header", len(message))
	}
	if len(message) > MaxMessage {
		return Frame{}, protocolError("message of %d bytes exceeds %d", len(message), MaxMessage)
	}
	frame := Frame{
		Type:     FrameType(message[0]),
		StreamID: binary.BigEndian.Uint32(message[1:frameHeaderSize]),
		Payload:  message[frameHeaderSize:],
	}
	if frame.StreamID == 0 || frame.StreamID%2 == 0 {
		return Frame{}, protocolError("stream id %d is not odd", frame.StreamID)
	}
	if err := checkPayload(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func checkPayload(frame Frame) error {
	size := len(frame.Payload)
	switch frame.Type {
	case FrameOpen:
		_, err := DecodeOpen(frame.Payload)
		return err
	case FrameData:
		if size == 0 {
			return protocolError("DATA payload is empty")
		}
	case FrameEnd:
		if size != 0 {
			return protocolError("END payload has %d bytes", size)
		}
	case FrameReset:
		if size != 1 {
			return protocolError("RESET payload has %d bytes", size)
		}
	case FrameWindow:
		if size != 4 {
			return protocolError("WINDOW payload has %d bytes", size)
		}
		credit := binary.BigEndian.Uint32(frame.Payload)
		if credit == 0 || credit > MaxWindow {
			return protocolError("WINDOW credit %d is outside 1..%d", credit, MaxWindow)
		}
	default:
		return protocolError("unknown frame type %d", frame.Type)
	}
	return nil
}

func DecodeOpen(payload []byte) (OpenRequest, error) {
	if len(payload) == 0 || len(payload) > maxOpenPayload {
		return OpenRequest{}, protocolError("OPEN payload of %d bytes is outside 1..%d", len(payload), maxOpenPayload)
	}
	if !utf8.Valid(payload) {
		return OpenRequest{}, protocolError("OPEN payload is not valid UTF-8")
	}
	fields, err := strictjson.Object(payload)
	if err != nil {
		return OpenRequest{}, protocolError("OPEN payload: %v", err)
	}
	keys := []string{openPreviewIDKey, openPreviewKeyName}
	if err := strictjson.ExactFields(fields, keys, keys); err != nil {
		return OpenRequest{}, protocolError("OPEN payload: %v", err)
	}
	values := make([]string, len(keys))
	for index, key := range keys {
		value, err := strictjson.String(fields[key])
		if err != nil {
			return OpenRequest{}, protocolError("OPEN %s: %v", key, err)
		}
		if value == "" {
			return OpenRequest{}, protocolError("OPEN %s is empty", key)
		}
		values[index] = value
	}
	return OpenRequest{PreviewID: values[0], PreviewKey: values[1]}, nil
}

func WindowCredit(frame Frame) uint32 {
	if len(frame.Payload) != 4 {
		return 0
	}
	return binary.BigEndian.Uint32(frame.Payload)
}

func ResetCodeOf(frame Frame) ResetCode {
	if len(frame.Payload) != 1 {
		return ResetProtocol
	}
	code := ResetCode(frame.Payload[0])
	if code < ResetNotFound || code > ResetProtocol {
		return ResetProtocol
	}
	return code
}
