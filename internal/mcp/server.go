package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Server is a minimal MCP stdio server implementing JSON-RPC 2.0.
type Server struct {
	tools    map[string]*Tool
	handlers map[string]Handler
}

// Tool describes an MCP tool.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Handler processes a tool call and returns text content.
type Handler func(args map[string]any) (string, error)

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Error is a JSON-RPC error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func New() *Server {
	return &Server{
		tools:    make(map[string]*Tool),
		handlers: make(map[string]Handler),
	}
}

// Register adds a tool to the server.
func (s *Server) Register(tool *Tool, handler Handler) {
	s.tools[tool.Name] = tool
	s.handlers[tool.Name] = handler
}

// Run starts the stdio JSON-RPC loop.
func (s *Server) Run() error {
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read error: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.writeError(writer, nil, -32700, "Parse error")
			continue
		}

		resp := s.handle(&req)
		s.writeResponse(writer, resp)
	}
}

func (s *Server) handle(req *Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInit(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "notifications/initialized":
		return nil // no response for notifications
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}
}

func (s *Server) handleInit(req *Request) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "sentinel-mcp",
				"version": "0.1.0",
			},
		},
	}
}

func (s *Server) handleToolsList(req *Request) *Response {
	var tools []map[string]any
	for _, t := range s.tools {
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": tools},
	}
}

func (s *Server) handleToolsCall(req *Request) *Response {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32602, Message: "Invalid params"},
		}
	}

	handler, ok := s.handlers[params.Name]
	if !ok {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32602, Message: "Unknown tool: " + params.Name},
		}
	}

	result, err := handler(params.Arguments)
	if err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "Error: " + err.Error()},
				},
				"isError": true,
			},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": result},
			},
		},
	}
}

func (s *Server) writeResponse(w io.Writer, resp *Response) {
	if resp == nil {
		return
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "%s\n", data)
}

func (s *Server) writeError(w io.Writer, id any, code int, msg string) {
	resp := &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: msg},
	}
	s.writeResponse(w, resp)
}
