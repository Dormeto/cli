package aws

// The ports of a region's discovery (discover.RegionPorts): the adapters are
// the region's own (ELB, Auto Scaling, EC2, ECS, RDS), one per operation. Ports
// manager packages/estimate/src/bin.ts (portsOf).

import (
	"context"
	"time"

	"github.com/Dormeto/cli/internal/discover"
)

// Discover is the ports of the region's discovery: the scan (ScanRegion with
// the region's ports, at now()), the Auto Scaling groups, rules by ARN, EC2 and
// what a nap stops. Make one Region per account and region (its EKS clusters
// are kept 15 minutes).
func (r *Region) Discover(where discover.Where, now func() time.Time) discover.RegionPorts {
	return discover.RegionPorts{
		Scan: func(ctx context.Context) (discover.ScanResult, error) {
			return discover.ScanRegion(ctx, r.Scan(), where, now())
		},
		AutoScaling: r.autoscaling,
		ELBv2:       r.elbv2,
		EC2:         stoppedEC2{r.ec2},
		Stopped:     r.Stopped(),
	}
}
