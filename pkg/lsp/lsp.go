// Package lsp implements a minimal, synchronous Language Server Protocol
// client that speaks JSON-RPC 2.0 over a server process's stdio. It is
// stdlib-only and intended for read-only symbol resolution: enclosing
// definition ranges, go-to-definition and find-references.
package lsp

import (
	"encoding/json"
	"fmt"
	"time"
)

// requestTimeout bounds how long a single request waits for its response.
const requestTimeout = 10 * time.Second

// Position is a zero-based location within a text document.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span between two positions (end exclusive).
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location identifies a range within a document URI.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// DocumentSymbol is a hierarchical symbol with an enclosing Range and a
// narrower SelectionRange (typically the name).
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// rpcRequest is an outgoing JSON-RPC request or notification.
type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int        `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// rpcResponse is an incoming JSON-RPC message. It may be a response (has id
// and result/error) or a server-to-client request/notification (has method).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}
