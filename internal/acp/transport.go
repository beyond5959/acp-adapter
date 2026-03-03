package acp

import "encoding/json"

// Transport is the ACP JSON-RPC message transport used by Server.
//
// Implementations must preserve JSON-RPC semantics and be safe for
// concurrent writes.
type Transport interface {
	ReadMessage() (RPCMessage, error)
	WriteMessage(msg RPCMessage) error
	WriteResult(id json.RawMessage, result any) error
	WriteError(id json.RawMessage, code int, message string, data any) error
	WriteNotification(method string, params any) error
}
