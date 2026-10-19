package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
)

type fakeAPIs struct{ pages []*apigatewayv2.GetApisOutput }

func (f *fakeAPIs) GetApis(ctx context.Context, in *apigatewayv2.GetApisInput, _ ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error) {
	i := 0
	if in.NextToken != nil {
		i = 1
	}
	return f.pages[i], nil
}

func str(s string) *string { return &s }

func TestAPIURLFoundOnSecondPage(t *testing.T) {
	l := &apiLocator{api: &fakeAPIs{pages: []*apigatewayv2.GetApisOutput{
		{Items: []types.Api{{Name: str("other"), ApiEndpoint: str("https://x")}}, NextToken: str("t")},
		{Items: []types.Api{{Name: str("dmt-prd-manager-api"), ApiEndpoint: str("https://abc.execute-api.eu-west-1.amazonaws.com")}}},
	}}}
	got, err := l.APIURL(context.Background(), "prd", "eu-west-1")
	if err != nil || got != "https://abc.execute-api.eu-west-1.amazonaws.com" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestAPIURLAbsent(t *testing.T) {
	l := &apiLocator{api: &fakeAPIs{pages: []*apigatewayv2.GetApisOutput{{}}}}
	_, err := l.APIURL(context.Background(), "dev", "eu-west-3")
	if !errors.Is(err, ErrNoAPI) || !strings.Contains(err.Error(), "dmt-dev-manager-api") || !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "eu-west-3") {
		t.Errorf("err = %v", err)
	}
}
