package kubernetes

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Ports router lambda/test/k8s.test.mjs: clusterOf.

type fakeEKS struct {
	mu       sync.Mutex
	calls    []string
	clusters map[string]*EKSCluster
	fail     func(n int) bool
	gate     chan struct{}
}

func (f *fakeEKS) ListClusters(context.Context, ListClustersInput) (ListClustersOutput, error) {
	return ListClustersOutput{}, nil
}

func (f *fakeEKS) DescribeCluster(_ context.Context, in DescribeClusterInput) (DescribeClusterOutput, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in.Name)
	c := f.clusters[in.Name]
	if c == nil || (f.fail != nil && f.fail(len(f.calls))) {
		return DescribeClusterOutput{}, errors.New("No cluster found for name: " + in.Name + ".")
	}
	return DescribeClusterOutput{Cluster: c}, nil
}

func demoCluster() *EKSCluster {
	return &EKSCluster{
		Name: "eks-demo", Version: "1.36", Endpoint: "https://ABC.gr7.eu-west-1.eks.amazonaws.com",
		CertificateAuthority: &CertificateAuthority{Data: base64.StdEncoding.EncodeToString([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))},
		ResourcesVpcConfig:   &VPCConfig{EndpointPublicAccess: true, PublicAccessCidrs: []string{"0.0.0.0/0"}},
		AccessConfig:         &AccessConfig{AuthenticationMode: "API"},
	}
}

var t0 = time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)

func TestClusterOfReadsTheEndpointTheDecodedCAAccessTheAuthModeAndTheVersion(t *testing.T) {
	v, err := ClusterOf(context.Background(), &fakeEKS{clusters: map[string]*EKSCluster{"eks-demo": demoCluster()}}, "eks-demo", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := ClusterView{
		Endpoint: "https://ABC.gr7.eu-west-1.eks.amazonaws.com", CA: []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"),
		PublicAccess: true, PublicCidrs: []string{"0.0.0.0/0"}, PrivateOnly: false, AuthMode: "API", Version: "1.36",
	}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("got %+v", v)
	}

	restricted := demoCluster()
	restricted.ResourcesVpcConfig.EndpointPublicAccess = false
	restricted.ResourcesVpcConfig.EndpointPrivateAccess = true
	restricted.ResourcesVpcConfig.PublicAccessCidrs = nil
	restricted.AccessConfig = nil
	v, err = ClusterOf(context.Background(), &fakeEKS{clusters: map[string]*EKSCluster{"eks-demo": restricted}}, "eks-demo", t0)
	if err != nil {
		t.Fatal(err)
	}
	if v.PublicAccess || len(v.PublicCidrs) != 0 || !v.PrivateOnly {
		t.Errorf("access: %+v", v)
	}
	if v.AuthMode != "CONFIG_MAP" {
		t.Errorf("a cluster without accessConfig predates access entries: %s", v.AuthMode)
	}
}

func TestClusterOfKeepsAClusterFor15MinutesPerEKSPortAndNameAndDoesNotKeepAFailure(t *testing.T) {
	if ClusterTTL != 15*time.Minute {
		t.Fatalf("ttl %v", ClusterTTL)
	}
	ctx := context.Background()
	eks := &fakeEKS{clusters: map[string]*EKSCluster{"eks-demo": demoCluster(), "other": demoCluster()}}
	for _, step := range []struct {
		name string
		at   time.Duration
		want []string
	}{
		{"eks-demo", 0, []string{"eks-demo"}},
		{"eks-demo", ClusterTTL - time.Millisecond, []string{"eks-demo"}},
		{"other", 0, []string{"eks-demo", "other"}},
		{"eks-demo", ClusterTTL, []string{"eks-demo", "other", "eks-demo"}},
	} {
		if _, err := ClusterOf(ctx, eks, step.name, t0.Add(step.at)); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(eks.calls, step.want) {
			t.Fatalf("after %s at +%v: calls %v, want %v", step.name, step.at, eks.calls, step.want)
		}
	}
	another := &fakeEKS{clusters: map[string]*EKSCluster{"eks-demo": demoCluster()}}
	if _, err := ClusterOf(ctx, another, "eks-demo", t0); err != nil || len(another.calls) != 1 {
		t.Fatalf("another EKS port keeps its own: %v %v", err, another.calls)
	}

	flaky := &fakeEKS{clusters: map[string]*EKSCluster{"eks-demo": demoCluster()}, fail: func(n int) bool { return n == 1 }}
	if _, err := ClusterOf(ctx, flaky, "eks-demo", t0); err == nil {
		t.Fatal("the first call should fail")
	}
	v, err := ClusterOf(ctx, flaky, "eks-demo", t0)
	if err != nil || v.Version != "1.36" || len(flaky.calls) != 2 {
		t.Fatalf("%+v %v %v", v, err, flaky.calls)
	}
}

func TestClusterOfSharesTheCallInFlight(t *testing.T) {
	eks := &fakeEKS{clusters: map[string]*EKSCluster{"eks-demo": demoCluster()}, gate: make(chan struct{})}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ClusterOf(context.Background(), eks, "eks-demo", t0); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(eks.gate)
	wg.Wait()
	if len(eks.calls) != 1 {
		t.Fatalf("calls %v", eks.calls)
	}
}
