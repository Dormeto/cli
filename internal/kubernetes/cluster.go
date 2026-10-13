package kubernetes

// Ports router lambda/src/lib/k8s.mjs: clusterOf, CLUSTER_TTL_MS, viewOf.

import (
	"context"
	"encoding/base64"
	"reflect"
	"sync"
	"time"
)

// ClusterTTL is how long a cluster's view is kept (15 minutes).
const ClusterTTL = 15 * time.Minute

// ClusterView is a cluster's endpoint, certificate authority, reach and
// authentication mode (the router's clusterOf).
type ClusterView struct {
	Endpoint     string
	CA           []byte // PEM, decoded
	PublicAccess bool
	PublicCidrs  []string
	PrivateOnly  bool
	AuthMode     string // CONFIG_MAP for a cluster without accessConfig
	Version      string
}

func viewOf(c *EKSCluster) ClusterView {
	if c == nil {
		c = &EKSCluster{}
	}
	v := ClusterView{Endpoint: c.Endpoint, Version: c.Version, AuthMode: "CONFIG_MAP", PublicCidrs: []string{}}
	if c.CertificateAuthority != nil {
		v.CA, _ = base64.StdEncoding.DecodeString(c.CertificateAuthority.Data)
	}
	if vpc := c.ResourcesVpcConfig; vpc != nil {
		v.PublicAccess = vpc.EndpointPublicAccess
		if vpc.PublicAccessCidrs != nil {
			v.PublicCidrs = vpc.PublicAccessCidrs
		}
	}
	v.PrivateOnly = !v.PublicAccess
	if c.AccessConfig != nil && c.AccessConfig.AuthenticationMode != "" {
		v.AuthMode = c.AccessConfig.AuthenticationMode
	}
	return v
}

type kept struct {
	at   time.Time
	done chan struct{}
	view ClusterView
	err  error
}

var (
	clustersMu sync.Mutex
	clusters   = map[any]map[string]*kept{}
)

// ClusterOf reads a cluster once per 15 minutes per EKS port and name (a
// concurrent caller shares the call in flight); a failure is not kept. A port
// that cannot be a map key (not comparable) is not cached.
func ClusterOf(ctx context.Context, eks EKSPort, name string, now time.Time) (ClusterView, error) {
	if !reflect.TypeOf(eks).Comparable() {
		return describe(ctx, eks, name)
	}
	clustersMu.Lock()
	byName := clusters[eks]
	if byName == nil {
		byName = map[string]*kept{}
		clusters[eks] = byName
	}
	if k := byName[name]; k != nil && now.Sub(k.at) < ClusterTTL {
		clustersMu.Unlock()
		<-k.done
		return k.view, k.err
	}
	k := &kept{at: now, done: make(chan struct{})}
	byName[name] = k
	clustersMu.Unlock()

	k.view, k.err = describe(ctx, eks, name)
	if k.err != nil {
		clustersMu.Lock()
		if byName[name] == k {
			delete(byName, name)
		}
		clustersMu.Unlock()
	}
	close(k.done)
	return k.view, k.err
}

func describe(ctx context.Context, eks EKSPort, name string) (ClusterView, error) {
	out, err := eks.DescribeCluster(ctx, DescribeClusterInput{Name: name})
	if err != nil {
		return ClusterView{}, err
	}
	return viewOf(out.Cluster), nil
}
