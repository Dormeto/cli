package aws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/Dormeto/cli/internal/discover"
)

// rulesAnswer is ELB's DescribeRules answer: two rules that differ only by an
// HTTP header's value, one by host and path, one by a query string without a
// key, and the default rule.
const rulesAnswer = `<DescribeRulesResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/">
  <DescribeRulesResult>
    <Rules>
      <member>
        <IsDefault>false</IsDefault>
        <Conditions>
          <member>
            <Field>http-header</Field>
            <HttpHeaderConfig><HttpHeaderName>X-Env</HttpHeaderName><Values><member>blue</member></Values></HttpHeaderConfig>
          </member>
        </Conditions>
        <Priority>10</Priority>
        <Actions>
          <member>
            <Type>forward</Type>
            <Order>1</Order>
            <TargetGroupArn>arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/blue/1</TargetGroupArn>
            <ForwardConfig>
              <TargetGroups>
                <member><TargetGroupArn>arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/blue/1</TargetGroupArn><Weight>1</Weight></member>
                <member><TargetGroupArn>arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/canary/1</TargetGroupArn></member>
              </TargetGroups>
            </ForwardConfig>
          </member>
        </Actions>
        <RuleArn>arn:rule/blue</RuleArn>
      </member>
      <member>
        <IsDefault>false</IsDefault>
        <Conditions>
          <member>
            <Field>http-header</Field>
            <HttpHeaderConfig><HttpHeaderName>X-Env</HttpHeaderName><Values><member>green</member></Values></HttpHeaderConfig>
          </member>
        </Conditions>
        <Priority>20</Priority>
        <Actions><member><Type>fixed-response</Type><Order>1</Order></member></Actions>
        <RuleArn>arn:rule/green</RuleArn>
      </member>
      <member>
        <IsDefault>false</IsDefault>
        <Conditions>
          <member>
            <Field>host-header</Field>
            <Values><member>a.example.com</member></Values>
            <HostHeaderConfig><Values><member>a.example.com</member></Values></HostHeaderConfig>
          </member>
          <member>
            <Field>path-pattern</Field>
            <PathPatternConfig><Values><member>/api/*</member></Values></PathPatternConfig>
          </member>
          <member>
            <Field>query-string</Field>
            <QueryStringConfig><Values><member><Value>v1</Value></member></Values></QueryStringConfig>
          </member>
        </Conditions>
        <Priority>30</Priority>
        <Actions><member><Type>redirect</Type></member></Actions>
        <RuleArn>arn:rule/host</RuleArn>
      </member>
      <member>
        <IsDefault>true</IsDefault>
        <Conditions/>
        <Priority>default</Priority>
        <Actions><member><Type>forward</Type><TargetGroupArn>arn:tg/default</TargetGroupArn></member></Actions>
        <RuleArn>arn:rule/default</RuleArn>
      </member>
    </Rules>
    <NextMarker>m2</NextMarker>
  </DescribeRulesResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</DescribeRulesResponse>`

func TestTheELBAdapterGivesEachConditionItsAWSJSONSoRulesDifferingByAHeaderDiffer(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		if r.Header.Get("Authorization") == "" {
			t.Error("the request is not signed")
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(rulesAnswer))
	}))
	defer srv.Close()
	cfg := awssdk.Config{Region: "eu-west-3", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: awssdk.String(srv.URL)}
	port := NewRegion(cfg, "eu-west-1", "").Scan().ELBv2

	out, err := port.DescribeRules(context.Background(), discover.DescribeRulesInput{ListenerArn: "arn:listener/1", Marker: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"Action": "DescribeRules", "ListenerArn": "arn:listener/1", "Marker": "m1"} {
		if got := form[k]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want %s", k, got, want)
		}
	}
	if out.NextMarker != "m2" || len(out.Rules) != 4 {
		t.Fatalf("%+v", out)
	}
	blue, green, host, def := out.Rules[0], out.Rules[1], out.Rules[2], out.Rules[3]

	// The key discovery compares rules by embeds the condition's JSON (scan.ts conditionsKey: JSON.stringify).
	wantBlue := `{"Field":"http-header","HttpHeaderConfig":{"HttpHeaderName":"X-Env","Values":["blue"]}}`
	if blue.Conditions[0].Raw != wantBlue {
		t.Errorf("blue: %s", blue.Conditions[0].Raw)
	}
	if green.Conditions[0].Raw == blue.Conditions[0].Raw {
		t.Errorf("two rules differing by a header compare equal: %s", green.Conditions[0].Raw)
	}
	if b, _ := json.Marshal(blue.Conditions[0]); string(b) != wantBlue {
		t.Errorf("the condition as discovery writes it: %s", b)
	}
	if got := host.Conditions[2].Raw; got != `{"Field":"query-string","QueryStringConfig":{"Values":[{"Value":"v1"}]}}` {
		t.Errorf("a query string without a key: %s", got)
	}
	if got := host.Conditions[0].Raw; got != `{"Field":"host-header","Values":["a.example.com"],"HostHeaderConfig":{"Values":["a.example.com"]}}` {
		t.Errorf("host: %s", got)
	}

	// The typed fields discovery reads.
	if c := host.Conditions[0]; c.Field != "host-header" || !reflect.DeepEqual(c.Values, []string{"a.example.com"}) || c.HostHeaderConfig == nil || !reflect.DeepEqual(c.HostHeaderConfig.Values, []string{"a.example.com"}) {
		t.Errorf("host: %+v", c)
	}
	if c := host.Conditions[1]; c.Values != nil || c.PathPatternConfig == nil || !reflect.DeepEqual(c.PathPatternConfig.Values, []string{"/api/*"}) {
		t.Errorf("path: %+v", c)
	}
	one := 1
	wantActions := []discover.Action{{Type: "forward", Order: 1, TargetGroupArn: "arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/blue/1", ForwardConfig: &discover.ForwardConfig{TargetGroups: []discover.ForwardTarget{
		{TargetGroupArn: "arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/blue/1", Weight: &one},
		{TargetGroupArn: "arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/canary/1"},
	}}}}
	if blue.RuleArn != "arn:rule/blue" || blue.Priority != "10" || blue.IsDefault || !reflect.DeepEqual(blue.Actions, wantActions) {
		t.Errorf("blue: %+v", blue)
	}
	if !def.IsDefault || def.Priority != "default" || len(def.Conditions) != 0 || def.Actions[0].ForwardConfig != nil || def.Actions[0].TargetGroupArn != "arn:tg/default" {
		t.Errorf("default: %+v", def)
	}
}

type fakeELB struct {
	elbv2API
	asked []any
}

func (f *fakeELB) DescribeLoadBalancers(_ context.Context, in *elb.DescribeLoadBalancersInput, _ ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error) {
	f.asked = append(f.asked, in)
	return &elb.DescribeLoadBalancersOutput{NextMarker: awssdk.String("n"), LoadBalancers: []elbtypes.LoadBalancer{{
		LoadBalancerArn: awssdk.String("arn:lb/app/web/1"), LoadBalancerName: awssdk.String("web"), Type: elbtypes.LoadBalancerTypeEnumApplication,
	}}}, nil
}

func (f *fakeELB) DescribeListeners(_ context.Context, in *elb.DescribeListenersInput, _ ...func(*elb.Options)) (*elb.DescribeListenersOutput, error) {
	f.asked = append(f.asked, in)
	return &elb.DescribeListenersOutput{Listeners: []elbtypes.Listener{{ListenerArn: awssdk.String("arn:listener/1"), Port: awssdk.Int32(443), Protocol: elbtypes.ProtocolEnumHttps}}}, nil
}

func (f *fakeELB) DescribeTags(_ context.Context, in *elb.DescribeTagsInput, _ ...func(*elb.Options)) (*elb.DescribeTagsOutput, error) {
	f.asked = append(f.asked, in)
	return &elb.DescribeTagsOutput{TagDescriptions: []elbtypes.TagDescription{{ResourceArn: awssdk.String("arn:lb/app/web/1"), Tags: []elbtypes.Tag{{Key: awssdk.String("env"), Value: awssdk.String("prod")}}}}}, nil
}

func (f *fakeELB) DescribeTargetGroups(_ context.Context, in *elb.DescribeTargetGroupsInput, _ ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error) {
	f.asked = append(f.asked, in)
	return &elb.DescribeTargetGroupsOutput{TargetGroups: []elbtypes.TargetGroup{{
		TargetGroupArn: awssdk.String("arn:tg/web/1"), TargetGroupName: awssdk.String("web"), TargetType: elbtypes.TargetTypeEnumIp,
		LoadBalancerArns: []string{"arn:lb/app/web/1"}, VpcId: awssdk.String("vpc-1"),
	}}}, nil
}

func (f *fakeELB) DescribeTargetHealth(_ context.Context, in *elb.DescribeTargetHealthInput, _ ...func(*elb.Options)) (*elb.DescribeTargetHealthOutput, error) {
	f.asked = append(f.asked, in)
	return &elb.DescribeTargetHealthOutput{TargetHealthDescriptions: []elbtypes.TargetHealthDescription{{Target: &elbtypes.TargetDescription{Id: awssdk.String("10.0.0.1")}}, {}}}, nil
}

func TestTheELBAdapterMapsTheOtherCallsFieldByField(t *testing.T) {
	f := &fakeELB{}
	p := elbv2Port{f}
	ctx := context.Background()
	lbs, _ := p.DescribeLoadBalancers(ctx, discover.DescribeLoadBalancersInput{})
	listeners, _ := p.DescribeListeners(ctx, discover.DescribeListenersInput{LoadBalancerArn: "arn:lb/app/web/1"})
	tags, _ := p.DescribeTags(ctx, discover.DescribeTagsInput{ResourceArns: []string{"arn:lb/app/web/1"}})
	groups, _ := p.DescribeTargetGroups(ctx, discover.DescribeTargetGroupsInput{Marker: "m"})
	health, _ := p.DescribeTargetHealth(ctx, discover.DescribeTargetHealthInput{TargetGroupArn: "arn:tg/web/1"})
	eqJSON(t, "load balancers", lbs, `{"LoadBalancers":[{"LoadBalancerArn":"arn:lb/app/web/1","LoadBalancerName":"web","Type":"application"}],"NextMarker":"n"}`)
	eqJSON(t, "listeners", listeners, `{"Listeners":[{"ListenerArn":"arn:listener/1","Port":443,"Protocol":"HTTPS"}]}`)
	eqJSON(t, "tags", tags, `{"TagDescriptions":[{"ResourceArn":"arn:lb/app/web/1","Tags":[{"Key":"env","Value":"prod"}]}]}`)
	eqJSON(t, "target groups", groups, `{"TargetGroups":[{"TargetGroupArn":"arn:tg/web/1","TargetGroupName":"web","TargetType":"ip","LoadBalancerArns":["arn:lb/app/web/1"],"VpcId":"vpc-1"}]}`)
	eqJSON(t, "targets", health, `{"TargetHealthDescriptions":[{"Target":{"Id":"10.0.0.1"}},{"Target":{}}]}`)
	want := []any{
		&elb.DescribeLoadBalancersInput{},
		&elb.DescribeListenersInput{LoadBalancerArn: awssdk.String("arn:lb/app/web/1")},
		&elb.DescribeTagsInput{ResourceArns: []string{"arn:lb/app/web/1"}},
		&elb.DescribeTargetGroupsInput{Marker: awssdk.String("m")},
		&elb.DescribeTargetHealthInput{TargetGroupArn: awssdk.String("arn:tg/web/1")},
	}
	if !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}

// eqJSON compares a port's output with its JSON.
func eqJSON(t *testing.T, what string, got any, want string) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Errorf("%s:\ngot  %s\nwant %s", what, b, want)
	}
}
