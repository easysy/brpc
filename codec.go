package brpc

import (
	"encoding/gob"
	"io"
	"log/slog"
)

// newCodec returns a new instance of a codec that uses gob encoding for message serialization.
func newCodec(conn io.ReadWriteCloser) *codec {
	return &codec{
		rwc: conn,
		dec: gob.NewDecoder(conn), // Create a new gob.Decoder to handle incoming data.
		enc: gob.NewEncoder(conn), // Create a new gob.Encoder to handle outgoing data.
	}
}

// codec wraps an io.ReadWriteCloser and uses gob for encoding/decoding messages.
type codec struct {
	rwc io.ReadWriteCloser
	dec *gob.Decoder
	enc *gob.Encoder
}

// write writes the provided message `v` to the io.ReadWriteCloser after encoding it with gob.
func (c *codec) write(v any) error {
	return c.enc.Encode(v)
}

// read reads a message from the io.ReadWriteCloser and decodes it using gob into the provided variable `v`.
func (c *codec) read(v any) error {
	return c.dec.Decode(v)
}

// close closes the io.ReadWriteCloser.
func (c *codec) close() {
	if err := c.rwc.Close(); err != nil {
		slog.Error("close resource", "error", err)
	}
}
