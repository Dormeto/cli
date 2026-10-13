package aws

// The Price List API in us-east-1 (prices.PricingPort). Ports manager
// packages/estimate/src/bin.ts (prices: PricingClient, GetProductsCommand).

import (
	"context"
	"encoding/json"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/Dormeto/cli/internal/prices"
)

type pricingAPI interface {
	GetProducts(context.Context, *pricing.GetProductsInput, ...func(*pricing.Options)) (*pricing.GetProductsOutput, error)
}

type pricingPort struct{ client pricingAPI }

// NewPricing is the Price List API, asked in us-east-1 with the profile's
// configuration, the SDK's standard retries.
func NewPricing(cfg awssdk.Config) prices.PricingPort {
	return &pricingPort{pricing.NewFromConfig(cfg, func(o *pricing.Options) { o.Region = global })}
}

// GetProducts asks one page, as the Node estimate (the price book asks 10
// products at most); each product is the JSON string the API gives.
func (p *pricingPort) GetProducts(ctx context.Context, in prices.GetProductsInput) (prices.GetProductsOutput, error) {
	ask := &pricing.GetProductsInput{ServiceCode: awssdk.String(in.ServiceCode)}
	if in.MaxResults > 0 {
		ask.MaxResults = awssdk.Int32(int32(in.MaxResults))
	}
	for _, f := range in.Filters {
		ask.Filters = append(ask.Filters, pricingtypes.Filter{Type: pricingtypes.FilterType(f.Type), Field: awssdk.String(f.Field), Value: awssdk.String(f.Value)})
	}
	out, err := p.client.GetProducts(ctx, ask)
	if err != nil {
		return prices.GetProductsOutput{}, err
	}
	var res prices.GetProductsOutput
	for _, product := range out.PriceList {
		quoted, err := json.Marshal(product)
		if err != nil {
			return prices.GetProductsOutput{}, err
		}
		res.PriceList = append(res.PriceList, quoted)
	}
	return res, nil
}
