package mcp

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

// echoTool is a tiny tool the server offers so the client can call it back.
func echoTool() tool.Tool {
	return tool.Tool{
		Name:        "echo",
		Description: "echo the text back",
		Class:       tool.ClassRead,
		Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(input, &v)
			return "echo: " + v.Text, nil
		},
	}
}

// dialServed runs a Server over pipes and returns a Client wired to it.
func dialServed(t *testing.T, tools ...tool.Tool) *Client {
	t.Helper()
	c2sR, c2sW := io.Pipe() // server reads, client writes
	s2cR, s2cW := io.Pipe() // client reads, server writes

	srv := NewServer("tomo", tools)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, c2sR, s2cW) }()

	c := newClient("tomo", newStreamTransport(s2cR, c2sW, nil))
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	return c
}

func TestServerRoundTrip(t *testing.T) {
	c := dialServed(t, echoTool())
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	infos, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "echo" {
		t.Fatalf("tools = %+v", infos)
	}
	out, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: hi" {
		t.Errorf("result = %q", out)
	}
}

func TestServerUnknownTool(t *testing.T) {
	c := dialServed(t, echoTool())
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallTool(ctx, "missing", nil); err == nil {
		t.Error("expected an error for an unknown tool")
	}
}
