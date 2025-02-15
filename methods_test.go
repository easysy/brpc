package brpc_test

import (
	"testing"

	"github.com/easysy/brpc"
)

func TestTypeDescription(t *testing.T) {
	desc := brpc.TypeDescription((*Test)(nil))
	equal(t, exp, desc)
}

type Test struct {
	TestName
	TestStruct TestStruct `json:"test_struct"`
}

type TestName map[int]*TestStruct

type TestStruct struct {
	Field string `json:"field"`
	Struct
	Structure Struct `json:"structure,omitempty"`
	Nested    `json:"nested,omitempty"`
	nested
}

type Struct struct {
	Field  string `json:"field,omitempty"`
	Field2 int    `json:"field_2,omitempty"`
}

type Nested struct {
	FieldN int `json:"field_n,omitempty"`
}

type nested struct {
	FieldN int `json:"field_n,omitempty"`
}

var exp = &brpc.Entity{
	Type: "struct",
	Fields: []brpc.Entity{
		{
			Name: "TestName", Type: "map[int]struct", Mandatory: true,
			Fields: []brpc.Entity{
				{Name: "field", Type: "string", Mandatory: true},
				{Name: "field_2", Type: "int"},
				{Name: "structure", Type: "struct",
					Fields: []brpc.Entity{
						{Name: "field", Type: "string"},
						{Name: "field_2", Type: "int"},
					},
				},
				{Name: "nested", Type: "struct",
					Fields: []brpc.Entity{
						{Name: "field_n", Type: "int"},
					},
				},
				{Name: "field_n", Type: "int"},
			},
		},
		{
			Name: "test_struct", Type: "struct", Mandatory: true,
			Fields: []brpc.Entity{
				{Name: "field", Type: "string", Mandatory: true},
				{Name: "field_2", Type: "int"},
				{Name: "structure", Type: "struct",
					Fields: []brpc.Entity{
						{Name: "field", Type: "string"},
						{Name: "field_2", Type: "int"},
					},
				},
				{Name: "nested", Type: "struct",
					Fields: []brpc.Entity{
						{Name: "field_n", Type: "int"},
					},
				},
				{Name: "field_n", Type: "int"},
			},
		},
	},
}
