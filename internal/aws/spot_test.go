package aws

// The case of manager packages/api/test/spot.test.ts: the Spot price history
// the price book reads (R9.5b), DescribeSpotPriceHistory from the profile's
// account, an EC2 client per region.

import (
	"context"
	"reflect"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/Dormeto/cli/internal/prices"
)

type sentSpot struct {
	region string
	input  *ec2.DescribeSpotPriceHistoryInput
}

type fakeSpotClient struct {
	region string
	sent   *[]sentSpot
}

func (f fakeSpotClient) DescribeSpotPriceHistory(_ context.Context, in *ec2.DescribeSpotPriceHistoryInput, _ ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error) {
	*f.sent = append(*f.sent, sentSpot{f.region, in})
	return &ec2.DescribeSpotPriceHistoryOutput{SpotPriceHistory: []ec2types.SpotPrice{{AvailabilityZone: awssdk.String(f.region + "a"), SpotPrice: awssdk.String("0.019")}}}, nil
}

func TestSpotAsksEachRegionWithItsOwnClientTheInputAsGiven(t *testing.T) {
	var sent []sentSpot
	var made []string
	port := newSpot(func(region string) spotAPI {
		made = append(made, region)
		return fakeSpotClient{region, &sent}
	})
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	in := prices.SpotHistoryInput{InstanceTypes: []string{"t3.medium"}, ProductDescriptions: []string{"Linux/UNIX"}, StartTime: start}
	out, err := port.History(context.Background(), "eu-west-1", in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []prices.SpotPrice{{AvailabilityZone: "eu-west-1a", SpotPrice: "0.019"}}; !reflect.DeepEqual(out.SpotPriceHistory, want) {
		t.Errorf("history %+v", out.SpotPriceHistory)
	}
	if _, err := port.History(context.Background(), "eu-west-3", in); err != nil {
		t.Fatal(err)
	}
	asked := &ec2.DescribeSpotPriceHistoryInput{InstanceTypes: []ec2types.InstanceType{"t3.medium"}, ProductDescriptions: []string{"Linux/UNIX"}, StartTime: &start}
	if want := []sentSpot{{"eu-west-1", asked}, {"eu-west-3", asked}}; !reflect.DeepEqual(sent, want) {
		t.Errorf("sent %+v", sent)
	}
	// Each region's client is made once.
	if _, err := port.History(context.Background(), "eu-west-1", in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(made, []string{"eu-west-1", "eu-west-3"}) {
		t.Errorf("clients made for %v", made)
	}
}

func TestSpotKeepsThePricesTimestamps(t *testing.T) {
	at := time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC)
	got := spotHistoryOf(&ec2.DescribeSpotPriceHistoryOutput{SpotPriceHistory: []ec2types.SpotPrice{{AvailabilityZone: awssdk.String("eu-west-1b"), SpotPrice: awssdk.String("0.02"), Timestamp: &at}}})
	if len(got.SpotPriceHistory) != 1 || got.SpotPriceHistory[0].Timestamp == nil || !got.SpotPriceHistory[0].Timestamp.Equal(at) {
		t.Errorf("%+v", got)
	}
}
