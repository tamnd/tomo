package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	hakopolicy "github.com/tamnd/hako/pkg/policy"
	hakobox "github.com/tamnd/hako/pkg/sandbox"
)

// confined runs the command inside an OS-enforced sandbox: a restricted
// filesystem, network off unless the mode allows it, dropped privileges. The
// enforcement is the kernel's, so a command that talks its way past the model
// still cannot read outside the working tree or reach the network it was not
// granted. The mode names one of the built-in postures.
func confined(mode string) (Sandbox, error) {
	wd := workdir()
	p, ok := hakopolicy.Preset(mode, wd)
	if !ok {
		return nil, fmt.Errorf("sandbox %q: no such preset", mode)
	}
	r, err := p.Resolve()
	if err != nil {
		return nil, fmt.Errorf("sandbox %q: %w", mode, err)
	}
	return &confinedBox{mode: mode, dir: wd, resolved: r}, nil
}

type confinedBox struct {
	mode     string
	dir      string
	resolved *hakopolicy.Resolved
}

func (b *confinedBox) Name() string { return b.mode }

func (b *confinedBox) Run(ctx context.Context, argv []string) (string, error) {
	var out combined
	res, err := hakobox.Run(ctx, b.resolved, hakobox.Command{
		Argv:   argv,
		Dir:    b.dir,
		Stdout: &out,
		Stderr: &out,
	})
	if err != nil {
		return out.String(), err
	}
	if res.TimedOut {
		return out.String(), context.DeadlineExceeded
	}
	if res.ExitCode != 0 {
		return out.String(), fmt.Errorf("exit status %d", res.ExitCode)
	}
	return out.String(), nil
}

// combined merges stdout and stderr into one buffer the way CombinedOutput
// does, guarded because the sandbox may write the two streams from separate
// goroutines.
type combined struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *combined) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *combined) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
