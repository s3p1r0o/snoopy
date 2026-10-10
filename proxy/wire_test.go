package main

import (
	"encoding/json"
	"testing"
)

// TestCanonicalRequestBytes pins the hand-encoded canonical Complete request
// (a single user text message) to the bytes the generated protobuf stubs
// produce, verified in 2026-10-09 testing against the generated rill.admin.v1
// and rill.ai.v1 definitions in github.com/rilldata/rill.
func TestCanonicalRequestBytes(t *testing.T) {
	req := &CompleteRequest{Messages: []Message{{Role: "user", Blocks: []Block{{Text: "Reply with the single word: pong"}}}}}
	b, err := marshalCompleteRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	const want = "0a2a0a04757365721a220a205265706c792077697468207468652073696e676c6520776f72643a20706f6e67"
	if got := hex(b); got != want {
		t.Fatalf("canonical request bytes differ:\n got %s\nwant %s", got, want)
	}
}

// TestStructRoundTrip checks ToolCall.input's Struct-based handling: a JSON
// object must survive the encode (Go values → wire) / decode (wire → Go
// values → JSON) path that requests and responses take.
func TestStructRoundTrip(t *testing.T) {
	in := `{"city":"Paris","days":[1,2,3],"nested":{"rain":true,"mm":2.5},"missing":null}`
	wire, err := structBytesFromJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonFromStructBytes(wire)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["city"]; !ok {
		t.Fatalf("round trip lost fields: %s", out)
	}
	if len(got) != len(want) {
		t.Fatalf("round trip changed field count: %d → %d (%s)", len(want), len(got), out)
	}
}

// TestResponseTokens checks response decoding of the token accounting fields.
func TestResponseTokens(t *testing.T) {
	// Encoded with the generated protobuf stubs of rill.admin.v1/rill.ai.v1
	// (github.com/rilldata/rill): message = 1 (role "user", one text block "pong"),
	// input_tokens = 250006, output_tokens = 8192, cached_input_tokens = 220800, provider = "openai".
	b := []byte{
		0x0a, 0x0e, 0x0a, 0x04, 0x75, 0x73, 0x65, 0x72, 0x1a, 0x06, 0x0a, 0x04, 0x70, 0x6f, 0x6e, 0x67,
		0x10, 0x96, 0xa1, 0x0f, 0x18, 0x80, 0x40, 0x20, 0x80, 0xbd, 0x0d, 0x2a, 0x06, 0x6f, 0x70, 0x65, 0x6e, 0x61, 0x69,
	}
	resp := &CompleteResponse{}
	if err := unmarshalCompleteResponse(b, resp); err != nil {
		t.Fatal(err)
	}
	if resp.InputTokens != 250006 || resp.OutputTokens != 8192 || resp.CachedInputTokens != 220800 || resp.Provider != "openai" {
		t.Fatalf("unexpected decode: %+v", resp)
	}
	if resp.Message.Role != "user" || len(resp.Message.Blocks) != 1 || resp.Message.Blocks[0].Text != "pong" {
		t.Fatalf("unexpected message decode: %+v", resp.Message)
	}
}

func hex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2], out[i*2+1] = digits[c>>4], digits[c&0x0f]
	}
	return string(out)
}
