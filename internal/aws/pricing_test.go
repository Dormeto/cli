package aws

import (
	"context"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/Dormeto/cli/internal/prices"
)

type fakePricing struct{ asked []*pricing.GetProductsInput }

func (f *fakePricing) GetProducts(_ context.Context, in *pricing.GetProductsInput, _ ...func(*pricing.Options)) (*pricing.GetProductsOutput, error) {
	f.asked = append(f.asked, in)
	return &pricing.GetProductsOutput{PriceList: []string{`{"terms":{"OnDemand":{}}}`}}, nil
}

func TestThePricingAdapterGivesEachProductAsAJSONStringAsTheNodeSDK(t *testing.T) {
	f := &fakePricing{}
	out, err := (&pricingPort{f}).GetProducts(context.Background(), prices.GetProductsInput{ServiceCode: "AmazonEC2", Filters: []prices.Filter{{Type: "TERM_MATCH", Field: "instanceType", Value: "t3.medium"}}, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.PriceList) != 1 || string(out.PriceList[0]) != `"{\"terms\":{\"OnDemand\":{}}}"` {
		t.Errorf("%s", out.PriceList)
	}
	want := &pricing.GetProductsInput{ServiceCode: awssdk.String("AmazonEC2"), MaxResults: awssdk.Int32(10), Filters: []pricingtypes.Filter{{Type: pricingtypes.FilterTypeTermMatch, Field: awssdk.String("instanceType"), Value: awssdk.String("t3.medium")}}}
	if !reflect.DeepEqual(f.asked, []*pricing.GetProductsInput{want}) {
		t.Errorf("asked %+v", f.asked[0])
	}
}
