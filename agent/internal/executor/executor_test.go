package executor

import (
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/caps"
)

func mustStruct(m map[string]interface{}) *structpb.Struct {
	s, err := structpb.NewStruct(m)
	if err != nil {
		panic(err)
	}
	return s
}

func TestGetString(t *testing.T) {
	p, _ := structpb.NewStruct(map[string]interface{}{"cmd": "echo hi", "n": 5})
	if got := getString(p, "cmd"); got != "echo hi" {
		t.Fatalf("getString = %q", got)
	}
	if got := getString(nil, "x"); got != "" {
		t.Fatalf("getString nil = %q", got)
	}
	if got := getInt(p, "n"); got != 5 {
		t.Fatalf("getInt = %d", got)
	}
}

func TestUnsupportedType(t *testing.T) {
	e := New(nil)
	res := e.Handle(&ecpv1.Command{TraceId: "t1", Type: ecpv1.CommandType_COMMAND_TYPE_NET_GET})
	if res.Status != ecpv1.ResultStatus_RESULT_STATUS_FAILED {
		t.Fatalf("expected FAILED, got %v", res.Status)
	}
	if res.TraceId != "t1" {
		t.Fatalf("trace lost: %q", res.TraceId)
	}
}

func TestShellMissingCommand(t *testing.T) {
	e := New(nil)
	res := e.Handle(&ecpv1.Command{TraceId: "t2", Type: ecpv1.CommandType_COMMAND_TYPE_SHELL})
	if res.Status != ecpv1.ResultStatus_RESULT_STATUS_FAILED {
		t.Fatalf("expected FAILED for missing command, got %v", res.Status)
	}
}

func TestDockerActionRejectsWithoutCapability(t *testing.T) {
	// 没有 docker 能力（零值 caps）时，写操作应被拒绝（不真正执行 docker）。
	e := &Executor{caps: &caps.Set{}}
	res := e.Handle(&ecpv1.Command{
		TraceId: "t3",
		Type:    ecpv1.CommandType_COMMAND_TYPE_DOCKER_ACTION,
		Params:  mustStruct(map[string]interface{}{"action": "stop", "container": "x"}),
	})
	if res.Status != ecpv1.ResultStatus_RESULT_STATUS_REJECTED {
		t.Fatalf("expected REJECTED for missing docker capability, got %v", res.Status)
	}
}
