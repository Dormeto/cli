package aws

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/Dormeto/cli/internal/kubernetes"
)

type fakeEKS struct {
	endpoint, ca string
	asked        []any
}

func (f *fakeEKS) ListClusters(_ context.Context, in *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	f.asked = append(f.asked, in)
	return &eks.ListClustersOutput{Clusters: []string{"demo"}, NextToken: awssdk.String("n")}, nil
}

func (f *fakeEKS) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	f.asked = append(f.asked, in)
	return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
		Name: in.Name, Version: awssdk.String("1.31"), Endpoint: awssdk.String(f.endpoint),
		CertificateAuthority: &ekstypes.Certificate{Data: awssdk.String(f.ca)},
		ResourcesVpcConfig:   &ekstypes.VpcConfigResponse{VpcId: awssdk.String("vpc-1"), EndpointPublicAccess: true, PublicAccessCidrs: []string{"0.0.0.0/0"}},
		AccessConfig:         &ekstypes.AccessConfigResponse{AuthenticationMode: ekstypes.AuthenticationModeApiAndConfigMap},
	}}, nil
}

func TestTheEKSAdapterMapsTheClusters(t *testing.T) {
	f := &fakeEKS{endpoint: "https://demo.eks", ca: "Q0E="}
	p := eksPort{f}
	list, err := p.ListClusters(context.Background(), kubernetes.ListClustersInput{NextToken: "t"})
	if err != nil || !reflect.DeepEqual(list, kubernetes.ListClustersOutput{Clusters: []string{"demo"}, NextToken: "n"}) {
		t.Errorf("%+v %v", list, err)
	}
	out, err := p.DescribeCluster(context.Background(), kubernetes.DescribeClusterInput{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	eqJSON(t, "cluster", out, `{"cluster":{"name":"demo","version":"1.31","endpoint":"https://demo.eks","certificateAuthority":{"data":"Q0E="},"resourcesVpcConfig":{"vpcId":"vpc-1","endpointPublicAccess":true,"publicAccessCidrs":["0.0.0.0/0"]},"accessConfig":{"authenticationMode":"API_AND_CONFIG_MAP"}}}`)
	if want := []any{&eks.ListClustersInput{NextToken: awssdk.String("t")}, &eks.DescribeClusterInput{Name: awssdk.String("demo")}}; !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked %+v", f.asked)
	}
}

func TestAClustersClientIsMadeFromItsEndpointItsCAAndAnEKSTokenOfTheCredentials(t *testing.T) {
	var auth string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"NamespaceList","items":[]}`))
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	f := &fakeEKS{endpoint: srv.URL, ca: ca}
	creds := credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session")
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	ports := kubernetesPorts(eksPort{f}, creds, "eu-west-1", "the profile", func() time.Time { return now })
	if ports.Reader != "the profile" {
		t.Errorf("reader %q", ports.Reader)
	}
	// A name of this test only: ClusterOf keeps a cluster 15 minutes per EKS port.
	client, err := ports.Of(context.Background(), "maker-demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "/api/v1/namespaces"); err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(auth, "Bearer k8s-aws-v1.")
	if !ok {
		t.Fatalf("authorization %q", auth)
	}
	presigned, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(string(presigned))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "sts.eu-west-1.amazonaws.com" || q.Get("Action") != "GetCallerIdentity" || !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIDEXAMPLE/20261013/eu-west-1/sts/") ||
		q.Get("X-Amz-Security-Token") != "session" || !strings.Contains(q.Get("X-Amz-SignedHeaders"), "x-k8s-aws-id") {
		t.Errorf("token %s", presigned)
	}
}

func TestAClustersClientNeedsCredentialsThatWork(t *testing.T) {
	f := &fakeEKS{endpoint: "https://demo.eks", ca: "Q0E="}
	failing := awssdk.CredentialsProviderFunc(func(context.Context) (awssdk.Credentials, error) {
		return awssdk.Credentials{}, &ssoExpired{}
	})
	ports := kubernetesPorts(eksPort{f}, failing, "eu-west-1", "", time.Now)
	if _, err := ports.Of(context.Background(), "maker-expired"); err == nil || err.Error() != "the SSO session has expired or is invalid" {
		t.Fatalf("%v", err)
	}
}

type ssoExpired struct{}

func (ssoExpired) Error() string { return "the SSO session has expired or is invalid" }
