package brpc

import (
	"encoding/gob"
	"encoding/json"
	"errors"
)

// init registers types for Gob encoding. This allows these types to be properly serialized
// and deserialized over a network connection using Gob encoding.
func init() {
	gob.Register(new(PluginInfo))
	gob.Register(new(Envelope))
}

const (
	MethodAsync    = "Async"
	MethodShutdown = "Shutdown"
)

var (
	ErrShutdown       = errors.New("connection is shut down")
	ErrMethodNotFound = errors.New("method not found")
)

// PluginInfo holds metadata about a plugin.
type PluginInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// AsyncData represents data received asynchronously from a plugin.
type AsyncData struct {
	Name    string `json:"name,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// Envelope represents a message structure used as an RPC call/return.
// It is used internally.
type Envelope struct {
	Seq     uint64
	Trace   string
	Method  string
	Error   string
	Payload []byte
}

// encode serializes the provided value into the Envelope's Payload using JSON encoding.
func (e *Envelope) encode(v any) (err error) {
	if v != nil {
		e.Payload, err = json.Marshal(v)
	}
	return
}

// decode deserializes the Envelope's Payload into the provided value using JSON decoding.
func (e *Envelope) decode(v any) (err error) {
	if e.Payload != nil {
		err = json.Unmarshal(e.Payload, v)
	}
	return
}
