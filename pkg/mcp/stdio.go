package mcp

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// stdioProcess ties a subprocess and its stdin together as one closer, so
// closing the client closes the pipe and stops the process.
type stdioProcess struct {
	cmd *exec.Cmd
	in  io.WriteCloser
}

// Close ends the server: close its stdin so it can exit cleanly, then make sure
// it is gone. The wait error after a kill is expected and dropped.
func (p *stdioProcess) Close() error {
	_ = p.in.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
	return nil
}

// StartStdio launches an MCP server as a subprocess and returns a client that
// speaks over its stdin and stdout. The server's stderr is forwarded to tomo's,
// so its own logging stays visible.
func StartStdio(ctx context.Context, name, command string, args []string, env map[string]string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return newClient(name, stdout, stdin, &stdioProcess{cmd: cmd, in: stdin}), nil
}
