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
	Name      string              `json:"name,omitempty"`
	Version   string              `json:"version,omitempty"`
	Functions map[string]Function `json:"functions,omitempty"`
}

// DeepCopy creates a copy of the PluginInfo instance.
// If `full` is true, it performs a deep copy of all Functions; otherwise, only Name and Version are copied.
func (p *PluginInfo) DeepCopy(full bool) *PluginInfo {
	c := &PluginInfo{
		Name:    p.Name,
		Version: p.Version,
	}

	if full {
		c.Functions = make(map[string]Function, len(p.Functions))
		for k, fn := range p.Functions {
			c.Functions[k] = fn.DeepCopy()
		}
	}

	return c
}

type Function struct {
	Name   string  `json:"name,omitempty"`
	Input  *Entity `json:"input,omitempty"`
	Output *Entity `json:"output,omitempty"`
}

func (f *Function) DeepCopy() Function {
	return Function{
		Name:   f.Name,
		Input:  f.Input.DeepCopy(),
		Output: f.Output.DeepCopy(),
	}
}

type Entity struct {
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type,omitempty"`
	Mandatory bool     `json:"mandatory,omitempty"`
	Fields    []Entity `json:"fields,omitempty"`
}

func (e *Entity) DeepCopy() *Entity {
	if e == nil {
		return nil
	}

	copyFields := make([]Entity, len(e.Fields))
	for i, field := range e.Fields {
		copyFields[i] = *field.DeepCopy()
	}

	return &Entity{
		Name:      e.Name,
		Type:      e.Type,
		Mandatory: e.Mandatory,
		Fields:    copyFields,
	}
}

func (e *Entity) merge(src *Entity) *Entity {
	if e == nil {
		return nil
	}

	if src == nil || e.Fields == nil {
		return e
	}

	origFieldMap := make(map[string]Entity)
	for i := range e.Fields {
		origFieldMap[e.Fields[i].Name] = e.Fields[i]
	}

	srcFieldMap := make(map[string]Entity)
	for i := range src.Fields {
		srcFieldMap[src.Fields[i].Name] = src.Fields[i]
	}

	for k, ov := range origFieldMap {
		sv, ok := srcFieldMap[k]
		if !ok {
			delete(origFieldMap, k)
			continue
		}
		ov.Mandatory = sv.Mandatory
		if len(ov.Fields) > 0 && len(sv.Fields) > 0 {
			ov = *ov.merge(&sv)
		}
		origFieldMap[k] = ov
	}

	var fields []Entity

	// Build final fields slice
	for _, field := range origFieldMap {
		fields = append(fields, field)
	}

	return &Entity{
		Name:      e.Name,
		Type:      e.Type,
		Mandatory: src.Mandatory,
		Fields:    fields,
	}
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
