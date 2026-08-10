package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type ToolCaller func(ctx context.Context, tool string, args json.RawMessage) (any, error)

type Server struct {
	Caller ToolCaller
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSocketRequestBytes)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := decodeStrict(line, &req); err != nil {
			_ = enc.Encode(Response{JSONRPC: "2.0", Error: &Error{Code: ErrParse, Message: err.Error()}})
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: ErrInvalid, Message: "invalid JSON-RPC request"}})
			continue
		}
		resp := s.handle(ctx, req)
		if resp == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req Request) *Response {
	switch req.Method {
	case "initialize":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "platform-mcp-bridge", "version": "0.1.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}}
	case "notifications/initialized":
		return nil
	case "tools/list":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": Tools}}
	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments json.RawMessage            `json:"arguments"`
			Meta      map[string]json.RawMessage `json:"_meta,omitempty"`
		}
		if err := decodeStrict(req.Params, &params); err != nil {
			return &Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: ErrParams, Message: err.Error()}}
		}
		if err := ValidateTool(params.Name); err != nil {
			return &Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: ErrParams, Message: err.Error()}}
		}
		if s.Caller == nil {
			return &Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: ErrInternal, Message: "tool bridge unavailable"}}
		}
		result, err := s.Caller(ctx, params.Name, params.Arguments)
		if err != nil {
			return &Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: ErrInternal, Message: truncateError(err.Error())}}
		}
		text, _ := json.Marshal(result)
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(text)}},
		}}
	}
	return &Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: ErrNoMethod, Message: fmt.Sprintf("unknown method %q", req.Method)}}
}
