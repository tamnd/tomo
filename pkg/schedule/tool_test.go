package schedule

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

func TestScheduleToolAddsJob(t *testing.T) {
	st := openStore(t)
	tl := Tool(st, "telegram", "c7")
	if tl.Name != "schedule" || tl.Class != tool.ClassWrite {
		t.Fatalf("tool = %s/%s", tl.Name, tl.Class)
	}

	out, err := tl.Run(context.Background(), json.RawMessage(`{"when":"@every 10m","prompt":"ping me"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "scheduled job") {
		t.Errorf("output = %q", out)
	}

	jobs, err := st.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %v err %v", jobs, err)
	}
	j := jobs[0]
	if j.Spec != "@every 10m" || j.Prompt != "ping me" || j.Channel != "telegram" || j.Chat != "c7" {
		t.Errorf("job = %+v", j)
	}
}

func TestScheduleToolRejectsBadInput(t *testing.T) {
	tl := Tool(openStore(t), "web", "c1")
	for _, in := range []string{
		`{"when":"","prompt":"x"}`,
		`{"when":"@every 10m","prompt":""}`,
		`{"when":"not a schedule","prompt":"x"}`,
	} {
		if _, err := tl.Run(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("Run(%s) should have failed", in)
		}
	}
}
