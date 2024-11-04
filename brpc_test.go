package brpc_test

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/easysy/brpc"
)

func equal(t *testing.T, exp, got interface{}) {
	t.Helper()
	if !reflect.DeepEqual(exp, got) {
		t.Fatalf("Not equal:\nexp: %v\ngot: %v", exp, got)
	}
}

type MockListener struct {
	cc chan net.Conn
}

func NewMockListener() *MockListener {
	return &MockListener{cc: make(chan net.Conn, 1)}
}

func (ml *MockListener) Accept() (net.Conn, error) {
	conn, ok := <-ml.cc
	if !ok {
		return nil, errors.New("listener closed")
	}
	return conn, nil
}

func (ml *MockListener) Close() error {
	close(ml.cc)
	return nil
}

func (ml *MockListener) Addr() net.Addr {
	return &net.TCPAddr{}
}

type MockConn struct {
	*io.PipeReader
	*io.PipeWriter
}

func NewMockConn() (*MockConn, *MockConn) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	return &MockConn{PipeReader: r1, PipeWriter: w2}, &MockConn{PipeReader: r2, PipeWriter: w1}
}

func (mc *MockConn) Close() error {
	if err := mc.PipeReader.Close(); err != nil {
		return err
	}
	return mc.PipeWriter.Close()
}

func (mc *MockConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (mc *MockConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (mc *MockConn) SetDeadline(time.Time) error      { return nil }
func (mc *MockConn) SetReadDeadline(time.Time) error  { return nil }
func (mc *MockConn) SetWriteDeadline(time.Time) error { return nil }

type FloatParams struct {
	A, B float64
}

type StringParams struct {
	A, B string
}

type Interface interface {
	Test() string
}

type TestType struct{}

func (s *TestType) Builtin(_ context.Context, in int) (string, error) {
	return strconv.Itoa(in), nil
}

func (s *TestType) Array(_ context.Context, in [3]string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, i)
	}
	return out, nil
}

func (s *TestType) Slice(_ context.Context, in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, i)
	}
	return out, nil
}

func (s *TestType) Struct(_ context.Context, in *StringParams) (*StringParams, error) {
	in.A = strings.ToUpper(in.A)
	in.B = strings.ToUpper(in.B)
	return in, nil
}

func (s *TestType) Map(_ context.Context, in map[string]string) (map[string]string, error) {
	in["C"] = "test"
	return in, nil
}

func (s *TestType) UnsuitableMethodInvalidInputTypeChan(_ context.Context, in chan string) (string, error) {
	return <-in, nil
}

func (s *TestType) UnsuitableMethodInvalidInputTypeFunc(_ context.Context, in func() string) (string, error) {
	return in(), nil
}

func (s *TestType) UnsuitableMethodInvalidInputTypeInterface(_ context.Context, in Interface) (string, error) {
	return in.Test(), nil
}

func (s *TestType) UnsuitableMethodInvalidOutputTypeChan(_ context.Context, in string) (chan string, error) {
	ch := make(chan string, 1)
	ch <- in
	return ch, nil
}

func (s *TestType) UnsuitableMethodInvalidOutputTypeFunc(_ context.Context, in string) (func() string, error) {
	return func() string { return in }, nil
}

func (s *TestType) UnsuitableMethodInvalidSignature(in *FloatParams) (float64, error) {
	return in.A + in.B, nil
}

func (s *TestType) privateMethod(context.Context, struct{}) (string, error) {
	return "", nil
}

func TestPlugin_Start(t *testing.T) {
	lis := NewMockListener()
	sock := new(brpc.Socket)
	sock.Serve(lis)

	pi := brpc.PluginInfo{
		Name:    "TestPlugin",
		Version: "v0.0.0",
	}

	go func() {
		conn, cc := NewMockConn()
		lis.cc <- cc

		plug := new(brpc.Plugin)
		err := plug.Start(new(TestType), &pi, conn, nil)
		equal(t, nil, err)
	}()

	for {
		if infos := sock.Connected(); len(infos) != 0 {
			equal(t, infos[0], pi)
			break
		}
	}

	tests := []struct {
		name string
		pl   string
		fn   string
		in   any
		out  any
		err  error
	}{
		{
			name: "Builtin type",
			pl:   "TestPlugin",
			fn:   "Builtin",
			in:   123,
			out:  "123",
		},
		{
			name: "Array",
			pl:   "TestPlugin",
			fn:   "Array",
			in:   []string{"word a", "word b", "word c", "word d", "word e"},
			out:  []any{"word a", "word b", "word c"},
		},
		{
			name: "Slice",
			pl:   "TestPlugin",
			fn:   "Slice",
			in:   []string{"word a", "word b"},
			out:  []any{"word a", "word b"},
		},
		{
			name: "Struct",
			pl:   "TestPlugin",
			fn:   "Struct",
			in:   &StringParams{A: "word a", B: "word b"},
			out:  map[string]any{"A": "WORD A", "B": "WORD B"},
		},
		{
			name: "Map",
			pl:   "TestPlugin",
			fn:   "Map",
			in:   &StringParams{A: "12.5", B: "1.25"},
			out:  map[string]any{"A": "12.5", "B": "1.25", "C": "test"},
		},
		{
			name: "Unsuitable method invalid input type chan",
			pl:   "TestPlugin",
			fn:   "UnsuitableMethodInvalidInputTypeChan",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "Unsuitable method invalid input type func",
			pl:   "TestPlugin",
			fn:   "UnsuitableMethodInvalidInputTypeFunc",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "Unsuitable method invalid input type interface",
			pl:   "TestPlugin",
			fn:   "UnsuitableMethodInvalidInputTypeInterface",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "Unsuitable method invalid output type chan",
			pl:   "TestPlugin",
			fn:   "UnsuitableMethodInvalidOutputTypeChan",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "Unsuitable method invalid output type func",
			pl:   "TestPlugin",
			fn:   "UnsuitableMethodInvalidOutputTypeFunc",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "Unsuitable method invalid signature",
			pl:   "TestPlugin",
			fn:   "UnsuitableMethodInvalidSignature",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "private method",
			pl:   "TestPlugin",
			fn:   "privateMethod",
			err:  brpc.ErrMethodNotFound,
		},
		{
			name: "Unavailable plugin",
			pl:   "UnavailablePlugin",
			fn:   "Builtin",
			err:  errors.New("plugin UnavailablePlugin not found"),
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := sock.Call(strconv.Itoa(i), tt.pl, tt.fn, tt.in)
			equal(t, err, tt.err)
			equal(t, res, tt.out)
		})
	}
}
