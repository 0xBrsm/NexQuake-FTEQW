package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
)

// JSON-RPC 2.0 error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidReq     = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	// Application-defined (JSON-RPC reserves -32000..-32099 for server errors).
	ErrCodeUnauthorized = -32000
	ErrCodeNotFound     = -32001
	ErrCodeConflict     = -32002
	ErrCodeDispatch     = -32003
	ErrCodeUnavailable  = -32004
)

// Request is a parsed JSON-RPC 2.0 request envelope.
type Request struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// Response is a JSON-RPC 2.0 response envelope.
type Response struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// RPCError is the JSON-RPC error object. Data is an optional structured
// addendum (per JSON-RPC 2.0); we use it to carry operator-facing hints
// like "Set rcon_password <secret>" alongside the canonical Message.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MethodError is returned by a command handler to propagate an application-
// level error with a specific JSON-RPC error code.
type MethodError struct {
	Code    int
	Message string
}

func (e *MethodError) Error() string { return e.Message }

// ParseRequest decodes a JSON-RPC envelope from body bytes. On failure returns
// a response populated with a parse-error object (id=null per spec).
func ParseRequest(body []byte) (*Request, *Response) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errorResponse(nil, ErrCodeInvalidReq, "empty request")
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errorResponse(nil, ErrCodeParseError, "invalid JSON")
	}
	if req.Jsonrpc != "2.0" {
		return nil, errorResponse(req.ID, ErrCodeInvalidReq, "jsonrpc must be \"2.0\"")
	}
	if strings.TrimSpace(req.Method) == "" {
		return nil, errorResponse(req.ID, ErrCodeInvalidReq, "method is required")
	}
	return &req, nil
}

// Dispatch runs a method against the registry and returns a response envelope.
// Auth is the caller's responsibility; Dispatch assumes client is already
// authorized for admin access. Handlers can still reject with MethodError if a
// specific target is unauthorized.
func (a *Admin) Dispatch(req *Request, client access.Client) *Response {
	cmd, ok := lookupCommand(req.Method)
	if !ok {
		msg := fmt.Sprintf("method %q not found", req.Method)
		a.auditRPC(client, req.Method, "error", msg)
		return errorResponse(req.ID, ErrCodeMethodNotFound, msg)
	}

	params, err := cmd.ParseParams(req.Params)
	if err != nil {
		a.auditRPC(client, req.Method, "error", err.Error())
		return errorResponse(req.ID, ErrCodeInvalidParams, err.Error())
	}

	a.auditRPC(client, req.Method, "request", params)

	result, err := cmd.Handler(a, client, params)
	if err != nil {
		var me *MethodError
		code := ErrCodeInternal
		if errors.As(err, &me) {
			code = me.Code
		}
		a.auditRPC(client, req.Method, "error", err.Error())
		return errorResponse(req.ID, code, err.Error())
	}

	a.auditRPC(client, req.Method, "result", result)
	return &Response{Jsonrpc: "2.0", Result: result, ID: req.ID}
}

func errorResponse(id json.RawMessage, code int, message string) *Response {
	return &Response{Jsonrpc: "2.0", Error: &RPCError{Code: code, Message: message}, ID: id}
}

// AuditUnauthorized records a valid RPC request that failed admin
// authorization before dispatch.
func (a *Admin) AuditUnauthorized(client access.Client, method string) {
	a.auditRPC(client, method, "error", "unauthorized")
}

// auditRPC emits one audit-log line for one RPC leg. direction is one of
// "request" / "result" / "error"; payload is JSON-marshaled. The line shape
// matches the format documented in src/docs/ADMIN.md "Audit Log":
//
//	admin-rcon <direction> actor=<id> method=<method> <direction>=<payload>
func (a *Admin) auditRPC(client access.Client, method, direction string, payload any) {
	if a == nil || a.audit == nil {
		return
	}
	bytes, _ := json.Marshal(payload)
	a.audit.Info(fmt.Sprintf("admin-rcon %s actor=%q method=%s %s=%s",
		direction, client.ID, method, direction, sanitizeAuditText(string(bytes))))
}

const auditTextMax = 512

func sanitizeAuditText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "<empty>"
	}
	if len(text) > auditTextMax {
		text = text[:auditTextMax-3] + "..."
	}
	return text
}
