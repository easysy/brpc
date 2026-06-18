package brpc

import (
	"encoding/gob"
	"encoding/json"
	"errors"
)

func init() {
	gob.Register(new(PluginInfo))
	gob.Register(new(Envelope))
}

const (
	// MethodAsync is the envelope method name used to deliver async plugin notifications.
	MethodAsync = "Async"
	// MethodShutdown is the envelope method name used to request or signal a plugin stop.
	MethodShutdown = "Shutdown"
)

var (
	// ErrShutdown is returned when a call is made after shutdown has been initiated.
	ErrShutdown = errors.New("connection is shut down")
	// ErrMethodNotFound is returned when the requested method is not registered on the plugin.
	ErrMethodNotFound = errors.New("method not found")
)

// PluginInfo holds identifying metadata for a plugin.
type PluginInfo struct {
	Name      string              `json:"name,omitempty"`
	Version   string              `json:"version,omitempty"`
	Functions map[string]Function `json:"functions,omitempty"`
}

// DeepCopy returns a copy of p.
// full=true copies the entire Functions map; false copies only Name and Version.
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

// Function describes a single method exposed by a plugin.
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

// Entity describes a Go type used as a method input or output.
// Nested composite types are represented via the Fields slice.
type Entity struct {
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type,omitempty"` // Go kind string, e.g. "struct", "int", "[]struct"
	Mandatory bool     `json:"mandatory,omitempty"`
	Fields    []Entity `json:"fields,omitempty"`
}

// DeepCopy returns an independent copy of e, or nil when e is nil.
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

// merge overlays user-supplied descriptions from src onto e.
// Fields present in e but absent in src are dropped from the result.
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

// AsyncData carries a notification delivered asynchronously from a plugin.
type AsyncData struct {
	Name    string `json:"name,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

type Envelope struct {
	Seq     uint64 // monotonically increasing ID matching a request to its response
	Trace   string // optional trace ID propagated from the caller
	Method  string // method name, or a control constant (MethodAsync, MethodShutdown)
	Error   string // non-empty when the plugin returned an error
	Payload []byte // JSON-encoded method argument or return value
}

func (e *Envelope) encode(v any) (err error) {
	if v != nil {
		e.Payload, err = json.Marshal(v)
	}
	return
}

func (e *Envelope) decode(v any) (err error) {
	if e.Payload != nil {
		err = json.Unmarshal(e.Payload, v)
	}
	return
}
