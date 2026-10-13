package aws

// EC2's Spot price history, from the profile's account, a client per region
// (prices.SpotPort). Ports manager packages/api/src/spot.ts (createSpotPort)
// and packages/estimate/src/bin.ts (prices: the profile's EC2 per region).

import (
	"context"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/Dormeto/cli/internal/prices"
)

type spotAPI interface {
	DescribeSpotPriceHistory(context.Context, *ec2.DescribeSpotPriceHistoryInput, ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error)
}

type spotPort struct {
	clientOf func(region string) spotAPI
	mu       sync.Mutex
	clients  map[string]spotAPI
}

// NewSpot is the Spot price history with the profile's configuration, an EC2
// client made per region when first asked, the SDK's standard retries.
func NewSpot(cfg awssdk.Config) prices.SpotPort {
	return newSpot(func(region string) spotAPI {
		return ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.Region = region })
	})
}

func newSpot(clientOf func(region string) spotAPI) *spotPort {
	return &spotPort{clientOf: clientOf, clients: map[string]spotAPI{}}
}

// History asks one page of the region's history, the input as given: an
// instance type the SDK does not list yet is still asked.
func (p *spotPort) History(ctx context.Context, region string, in prices.SpotHistoryInput) (prices.SpotHistoryOutput, error) {
	p.mu.Lock()
	client, ok := p.clients[region]
	if !ok {
		client = p.clientOf(region)
		p.clients[region] = client
	}
	p.mu.Unlock()
	ask := &ec2.DescribeSpotPriceHistoryInput{ProductDescriptions: in.ProductDescriptions}
	for _, t := range in.InstanceTypes {
		ask.InstanceTypes = append(ask.InstanceTypes, ec2types.InstanceType(t))
	}
	if !in.StartTime.IsZero() {
		start := in.StartTime
		ask.StartTime = &start
	}
	out, err := client.DescribeSpotPriceHistory(ctx, ask)
	if err != nil {
		return prices.SpotHistoryOutput{}, err
	}
	return spotHistoryOf(out), nil
}

func spotHistoryOf(out *ec2.DescribeSpotPriceHistoryOutput) prices.SpotHistoryOutput {
	var res prices.SpotHistoryOutput
	for _, s := range out.SpotPriceHistory {
		res.SpotPriceHistory = append(res.SpotPriceHistory, prices.SpotPrice{
			AvailabilityZone: awssdk.ToString(s.AvailabilityZone), SpotPrice: awssdk.ToString(s.SpotPrice), Timestamp: s.Timestamp,
		})
	}
	return res
}
