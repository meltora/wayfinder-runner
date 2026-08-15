package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type ToolCaller func(ctx context.Context, tool string, args json.RawMessage) (any, error)
type ToolLister func(ctx context.Context) ([]ToolDef, error)

type Server struct {
	Caller    ToolCaller
	ListTools ToolLister
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSocketRequestBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := decodeStrict(line, &req); err != nil {
			if err := writeResponse(w, &Response{JSONRPC: "2.0", Error: &Error{Code: ErrParse, Message: err.Error()}}); err != nil {
				return err
			}
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" || len(req.Method) > 128 {
			if err := writeResponse(w, &Response{JSONRPC: "2.0", Error: &Error{Code: ErrInvalid, Message: "invalid JSON-RPC request"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			if req.Method == "notifications/initialized" {
				_ = s.handle(ctx, req)
			}
			continue
		}
		if err := ValidateRequestID(req.ID); err != nil {
			if err := writeResponse(w, &Response{JSONRPC: "2.0", Error: &Error{Code: ErrInvalid, Message: err.Error()}}); err != nil {
				return err
			}
			continue
		}
		resp := s.handle(ctx, req)
		if resp == nil {
			continue
		}
		if err := writeResponse(w, resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func writeResponse(writer io.Writer, response *Response) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(raw) > maxSocketResponseBytes {
		raw, err = json.Marshal(&Response{
			JSONRPC: "2.0", ID: response.ID,
			Error: &Error{Code: ErrInternal, Message: "tool response exceeds the local MCP limit"},
		})
		if err != nil {
			return err
		}
	}
	raw = append(raw, '\n')
	_, err = writer.Write(raw)
	return err
}

func (s *Server) handle(ctx context.Context, req Request) *Response {
	id := any(json.RawMessage(req.ID))
	switch req.Method {
	case "initialize":
		return &Response{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "platform-mcp-bridge", "version": "0.1.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}}
	case "notifications/initialized":
		return nil
	case "tools/list":
		if err := requireEmptyParams(req.Params); err != nil {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrParams, Message: err.Error()}}
		}
		definitions, err := s.toolDefinitions(ctx)
		if err != nil {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrInternal, Message: "tool discovery unavailable"}}
		}
		return &Response{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": definitions}}
	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments json.RawMessage            `json:"arguments"`
			Meta      map[string]json.RawMessage `json:"_meta,omitempty"`
		}
		if err := decodeStrict(req.Params, &params); err != nil {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrParams, Message: err.Error()}}
		}
		definitions, err := s.toolDefinitions(ctx)
		if err != nil {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrInternal, Message: "tool discovery unavailable"}}
		}
		if !toolIsDefined(definitions, params.Name) {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrParams, Message: "tool is unavailable for this attempt"}}
		}
		if s.Caller == nil {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrInternal, Message: "tool bridge unavailable"}}
		}
		result, err := s.Caller(ctx, params.Name, params.Arguments)
		if err != nil {
			return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrInternal, Message: truncateError(err.Error())}}
		}
		text, _ := json.Marshal(result)
		return &Response{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(text)}},
		}}
	}
	return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: ErrNoMethod, Message: "unknown method"}}
}

func (s *Server) toolDefinitions(ctx context.Context) ([]ToolDef, error) {
	if s.ListTools != nil {
		return s.ListTools(ctx)
	}
	return ToolDefinitions(), nil
}

func toolIsDefined(definitions []ToolDef, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func requireEmptyParams(params json.RawMessage) error {
	if len(params) == 0 {
		return nil
	}
	object, err := DecodeToolObject(params)
	if err != nil {
		return err
	}
	if len(object) != 0 {
		return fmt.Errorf("method parameters must be empty")
	}
	return nil
}
