package fleet

import (
	"encoding/json"
	"testing"
)

func TestExclusionARNsFollowTheManagersShapes(t *testing.T) {
	tg := "arn:aws:elasticloadbalancing:eu-west-1:111111111111:targetgroup/api/0123456789abcdef"
	lb := "arn:aws:elasticloadbalancing:eu-west-1:111111111111:loadbalancer/app/my-lb/0123456789abcdef"
	if !IsTargetGroupARN(tg) || IsTargetGroupARN(lb) || !IsLoadBalancerARN(lb) || IsLoadBalancerARN(tg) || IsTargetGroupARN(tg+"0") {
		t.Error("ARN shapes")
	}
}

func TestOperationWorkloadFromTargetOrContext(t *testing.T) {
	var op Operation
	_ = json.Unmarshal([]byte(`{"id":"o","type":"onboard","context":{"workloadId":"w-1","router":{"name":"r1"}}}`), &op)
	if r, id := op.Workload(); r != "r1" || id != "w-1" {
		t.Errorf("context: %q %q", r, id)
	}
	_ = json.Unmarshal([]byte(`{"id":"o","type":"offboard","target":{"router":"r2","workloadId":"w-2"}}`), &op)
	if r, id := op.Workload(); r != "r2" || id != "w-2" {
		t.Errorf("target: %q %q", r, id)
	}
}

func TestExclusionTargetText(t *testing.T) {
	if got := (ExclusionTarget{Kind: "account", Account: "1", Name: "sandbox"}).Text(); got != "account sandbox (1)" {
		t.Error(got)
	}
	if got := (ExclusionTarget{Kind: "load-balancer", Name: "lb"}).Text(); got != "load balancer lb" {
		t.Error(got)
	}
}
