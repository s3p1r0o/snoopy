package main

// wire.go is a hand-written, minimal protobuf wire implementation of the subset of
// Rill's contract this proxy needs: the RPC `rill.admin.v1.AIService/Complete` plus
// the `rill.ai.v1` messages it carries. The field numbers are transcribed from the
// .proto sources copied in ./contract/. Keeping the encoding local (instead of
// generated stubs) is what makes this tool compile without any Rill dependency.

import (
	"encoding/json"
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// Types mirroring rill.ai.v1 and rill.admin.v1.

type Tool struct {
	Name        string
	Description string
	InputSchema string // JSON schema, as a JSON string
}

type ToolCall struct {
	ID    string
	Name  string
	Input string // argument object, as a raw JSON string ("" when absent)
}

type ToolResult struct {
	ID      string
	Content string
}

// Block mirrors rill.ai.v1.ContentBlock: one of text, tool_call, or tool_result.
type Block struct {
	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

// Message mirrors rill.ai.v1.CompletionMessage.
type Message struct {
	Role   string
	Data   string // deprecated legacy plain-text content, decoded if present
	Blocks []Block
}

type CompleteRequest struct {
	Messages []Message
	Tools    []Tool
}

type CompleteResponse struct {
	Message           *Message
	InputTokens       uint32
	OutputTokens      uint32
	CachedInputTokens uint32
	Provider          string
}

// The gRPC method and its codec, so requests can be sent without generated stubs.
const completeMethod = "/rill.admin.v1.AIService/Complete"

type rillCodec struct{}

// Name must stay "proto": grpc-go derives the HTTP/2 Content-Type from it
// ("application/grpc+proto"), which is what the server (and any edge in front
// of it) accepts. The payload bytes are genuine protobuf either way.
func (rillCodec) Name() string { return "proto" }

func (rillCodec) Marshal(v any) ([]byte, error) {
	switch r := v.(type) {
	case *CompleteRequest:
		return marshalCompleteRequest(r)
	}
	return nil, fmt.Errorf("rillCodec: unsupported type %T", v)
}

func (rillCodec) Unmarshal(data []byte, v any) error {
	switch r := v.(type) {
	case *CompleteResponse:
		return unmarshalCompleteResponse(data, r)
	}
	return fmt.Errorf("rillCodec: unsupported type %T", v)
}

// Encoders. See contract/rill.admin.v1.ai.proto: CompleteRequest is
// messages (CompletionMessage) = 1, tools (Tool) = 2, output_json_schema = 3.
func marshalCompleteRequest(req *CompleteRequest) ([]byte, error) {
	var out []byte
	for i := range req.Messages {
		b, err := marshalMessage(&req.Messages[i])
		if err != nil {
			return nil, err
		}
		out = appendFieldBytes(out, 1, b)
	}
	for _, t := range req.Tools {
		out = appendFieldBytes(out, 2, marshalTool(t))
	}
	return out, nil
}

func marshalMessage(m *Message) ([]byte, error) {
	var out []byte
	out = appendFieldString(out, 1, m.Role) // rill.ai.v1.CompletionMessage.role
	if len(m.Blocks) == 0 {
		if m.Data != "" {
			out = appendFieldString(out, 2, m.Data) // deprecated data field
		}
		return out, nil
	}
	for _, blk := range m.Blocks {
		b, err := marshalBlock(&blk)
		if err != nil {
			return nil, err
		}
		out = appendFieldBytes(out, 3, b) // repeated content
	}
	return out, nil
}

func marshalBlock(b *Block) ([]byte, error) {
	switch {
	case b.ToolCall != nil:
		var tc []byte
		tc = appendFieldString(tc, 1, b.ToolCall.ID)
		tc = appendFieldString(tc, 2, b.ToolCall.Name)
		if b.ToolCall.Input != "" {
			sb, err := structBytesFromJSON(b.ToolCall.Input)
			if err != nil {
				return nil, fmt.Errorf("tool call %q: %w", b.ToolCall.Name, err)
			}
			tc = appendFieldBytes(tc, 3, sb)
		}
		return appendFieldBytes(nil, 2, tc), nil // oneof tool_call
	case b.ToolResult != nil:
		var tr []byte
		tr = appendFieldString(tr, 1, b.ToolResult.ID)
		tr = appendFieldString(tr, 2, b.ToolResult.Content)
		return appendFieldBytes(nil, 3, tr), nil // oneof tool_result
	default:
		return appendFieldString(nil, 1, b.Text), nil // oneof text
	}
}

func marshalTool(t Tool) []byte {
	var out []byte
	out = appendFieldString(out, 1, t.Name)        // Tool.name
	out = appendFieldString(out, 2, t.Description) // Tool.description
	out = appendFieldString(out, 3, t.InputSchema) // Tool.input_schema
	return out
}

func appendFieldBytes(out []byte, num int, b []byte) []byte {
	out = protowire.AppendTag(out, protowire.Number(num), protowire.BytesType)
	return protowire.AppendBytes(out, b)
}

func appendFieldString(out []byte, num int, s string) []byte {
	out = protowire.AppendTag(out, protowire.Number(num), protowire.BytesType)
	return protowire.AppendString(out, s)
}

// Decoders. See contract/rill.admin.v1.ai.proto: CompleteResponse is
// message = 1, input_tokens = 2, output_tokens = 3, cached_input_tokens = 4, provider = 5.
func unmarshalCompleteResponse(data []byte, resp *CompleteResponse) error {
	msg := &Message{}
	err := walkFields(data, func(num protowire.Number, typ protowire.Type, payload []byte, val uint64) error {
		switch {
		case typ == protowire.BytesType && num == 1:
			m, err := parseMessage(payload)
			if err != nil {
				return err
			}
			msg = m
		case typ == protowire.BytesType && num == 5:
			resp.Provider = string(payload)
		case typ == protowire.VarintType && num == 2:
			resp.InputTokens = uint32(val)
		case typ == protowire.VarintType && num == 3:
			resp.OutputTokens = uint32(val)
		case typ == protowire.VarintType && num == 4:
			resp.CachedInputTokens = uint32(val)
		}
		return nil // skip unknown fields, for forward compatibility
	})
	if err != nil {
		return err
	}
	resp.Message = msg
	return nil
}

func parseMessage(b []byte) (*Message, error) {
	m := &Message{}
	err := walkFields(b, func(num protowire.Number, typ protowire.Type, payload []byte, val uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			m.Role = string(payload)
		case 2:
			m.Data = string(payload) // deprecated legacy content field
		case 3:
			blk, err := parseBlock(payload)
			if err != nil {
				return err
			}
			m.Blocks = append(m.Blocks, *blk)
		}
		return nil
	})
	return m, err
}

func parseBlock(b []byte) (*Block, error) {
	blk := &Block{}
	err := walkFields(b, func(num protowire.Number, typ protowire.Type, payload []byte, val uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1: // oneof text
			blk.Text = string(payload)
		case 2: // oneof tool_call
			tc := &ToolCall{}
			err := walkFields(payload, func(n2 protowire.Number, t2 protowire.Type, p2 []byte, v2 uint64) error {
				if t2 == protowire.BytesType && n2 == 1 {
					tc.ID = string(p2)
				}
				if t2 == protowire.BytesType && n2 == 2 {
					tc.Name = string(p2)
				}
				if t2 == protowire.BytesType && n2 == 3 {
					j, err := jsonFromStructBytes(p2)
					if err != nil {
						return err
					}
					tc.Input = string(j)
				}
				return nil
			})
			if err != nil {
				return err
			}
			blk.ToolCall = tc
		case 3: // oneof tool_result
			tr := &ToolResult{}
			err := walkFields(payload, func(n2 protowire.Number, t2 protowire.Type, p2 []byte, v2 uint64) error {
				if t2 == protowire.BytesType && n2 == 1 {
					tr.ID = string(p2)
				}
				if t2 == protowire.BytesType && n2 == 2 {
					tr.Content = string(p2)
				}
				return nil
			})
			if err != nil {
				return err
			}
			blk.ToolResult = tr
		}
		return nil
	})
	return blk, err
}

// walkFields iterates the wire fields of b, calling fn for each. For VarintType
// and Fixed64Type the value is in val; for BytesType it is in payload. Values
// of other wire types (rare; none occur in this contract) are skipped.
func walkFields(b []byte, fn func(num protowire.Number, typ protowire.Type, payload []byte, val uint64) error) error {
	rest := b
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return fmt.Errorf("malformed protobuf wire data")
		}
		rest = rest[n:]
		var payload []byte
		var val uint64
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(rest)
			if m < 0 {
				return fmt.Errorf("malformed protobuf wire data")
			}
			rest, val = rest[m:], v
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(rest)
			if m < 0 {
				return fmt.Errorf("malformed protobuf wire data")
			}
			rest, val = rest[m:], v
		case protowire.BytesType:
			p, m := protowire.ConsumeBytes(rest)
			if m < 0 {
				return fmt.Errorf("malformed protobuf wire data")
			}
			rest, payload = rest[m:], p
		default:
			m := protowire.ConsumeFieldValue(num, typ, rest)
			if m < 0 {
				return fmt.Errorf("malformed protobuf wire data")
			}
			rest = rest[m:]
			continue
		}
		if err := fn(num, typ, payload, val); err != nil {
			return err
		}
	}
	return nil
}

// Struct (google.protobuf.Struct) mapping, needed for ToolCall.input.

// structBytesFromJSON parses a JSON object string and encodes it as a google.protobuf.Struct.
func structBytesFromJSON(s string) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(s), &root); err != nil {
		return nil, err
	}
	return structBytes(root), nil
}

// wire layout of google.protobuf.Struct: repeated map entries { key = 1, value = 2 } under field 1.
func structBytes(m map[string]any) []byte {
	var out []byte
	for k, v := range m {
		vb, _ := valueBytes(v)
		var entry []byte
		entry = appendFieldString(entry, 1, k)
		entry = appendFieldBytes(entry, 2, vb)
		out = appendFieldBytes(out, 1, entry)
	}
	return out
}

// jsonFromStructBytes decodes a google.protobuf.Struct and re-encodes it as JSON.
func jsonFromStructBytes(wire []byte) ([]byte, error) {
	m, err := structFromBytes(wire)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func structFromBytes(wire []byte) (map[string]any, error) {
	out := make(map[string]any)
	err := walkFields(wire, func(num protowire.Number, typ protowire.Type, payload []byte, val uint64) error {
		if typ != protowire.BytesType || num != 1 {
			return nil
		}
		var key string
		var item any
		err := walkFields(payload, func(n2 protowire.Number, t2 protowire.Type, p2 []byte, v2 uint64) error {
			if t2 == protowire.BytesType && n2 == 1 {
				key = string(p2)
			}
			if t2 == protowire.BytesType && n2 == 2 {
				var err error
				item, err = valueFromBytes(p2)
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		if key != "" {
			out[key] = item
		}
		return nil
	})
	return out, err
}

// wire layout of google.protobuf.Value: oneof kind — null = 1 (enum), double = 2,
// string = 3, bool = 4, struct = 5, list = 6.
func valueBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		// null_value is an enum whose zero value still needs encoding (oneof field).
		out := protowire.AppendTag(nil, protowire.Number(1), protowire.VarintType)
		return protowire.AppendVarint(out, 0), nil
	case float64:
		out := protowire.AppendTag(nil, protowire.Number(2), protowire.Fixed64Type)
		return protowire.AppendFixed64(out, math.Float64bits(x)), nil
	case string:
		return appendFieldString(nil, 3, x), nil
	case bool:
		out := protowire.AppendTag(nil, protowire.Number(4), protowire.VarintType)
		if x {
			return protowire.AppendVarint(out, 1), nil
		}
		return protowire.AppendVarint(out, 0), nil
	case map[string]any:
		return appendFieldBytes(nil, 5, structBytes(x)), nil
	case []any:
		var lv []byte
		for _, item := range x {
			ib, err := valueBytes(item)
			if err != nil {
				return nil, err
			}
			lv = appendFieldBytes(lv, 1, ib)
		}
		return appendFieldBytes(nil, 6, lv), nil
	}
	return nil, fmt.Errorf("unencodable JSON value of type %T", v)
}

func valueFromBytes(b []byte) (any, error) {
	var out any
	err := walkFields(b, func(num protowire.Number, typ protowire.Type, payload []byte, val uint64) error {
		switch {
		case typ == protowire.VarintType && num == 1:
			out = nil // null_value
		case typ == protowire.Fixed64Type && num == 2:
			out = math.Float64frombits(val)
		case typ == protowire.BytesType && num == 3:
			out = string(payload)
		case typ == protowire.VarintType && num == 4:
			out = val != 0
		case typ == protowire.BytesType && num == 5:
			m, err := structFromBytes(payload)
			if err != nil {
				return err
			}
			out = m
		case typ == protowire.BytesType && num == 6:
			var list []any
			err := walkFields(payload, func(itypNum protowire.Number, ityp protowire.Type, ipayload []byte, ival uint64) error {
				if ityp == protowire.BytesType {
					v, err := valueFromBytes(ipayload)
					if err != nil {
						return err
					}
					list = append(list, v)
				}
				return nil
			})
			if err != nil {
				return err
			}
			out = list
		}
		return nil
	})
	return out, err
}
