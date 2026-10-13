package aws

// EKS: a region's clusters (kubernetes.EKSPort) and the client of a cluster's
// API, made from its endpoint and certificate authority (DescribeCluster, kept
// 15 minutes) and an EKS token signed with the account's credentials. Ports
// manager packages/discovery/src/aws.ts (kubernetesPorts) and router
// lambda/src/lib/k8s.mjs (kubernetesConnector).

import (
	"context"
	"errors"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/Dormeto/cli/internal/kubernetes"
)

type eksAPI interface {
	ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
}

// eksPort is kubernetes.EKSPort. A value of it is comparable: kubernetes.ClusterOf
// keeps a cluster per port.
type eksPort struct{ client eksAPI }

func (p eksPort) ListClusters(ctx context.Context, in kubernetes.ListClustersInput) (kubernetes.ListClustersOutput, error) {
	out, err := p.client.ListClusters(ctx, &eks.ListClustersInput{NextToken: optional(in.NextToken)})
	if err != nil {
		return kubernetes.ListClustersOutput{}, err
	}
	return kubernetes.ListClustersOutput{Clusters: out.Clusters, NextToken: awssdk.ToString(out.NextToken)}, nil
}

func (p eksPort) DescribeCluster(ctx context.Context, in kubernetes.DescribeClusterInput) (kubernetes.DescribeClusterOutput, error) {
	out, err := p.client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: optional(in.Name)})
	if err != nil {
		return kubernetes.DescribeClusterOutput{}, err
	}
	c := out.Cluster
	if c == nil {
		return kubernetes.DescribeClusterOutput{}, nil
	}
	cluster := &kubernetes.EKSCluster{Name: awssdk.ToString(c.Name), Version: awssdk.ToString(c.Version), Endpoint: awssdk.ToString(c.Endpoint)}
	if ca := c.CertificateAuthority; ca != nil {
		cluster.CertificateAuthority = &kubernetes.CertificateAuthority{Data: awssdk.ToString(ca.Data)}
	}
	if v := c.ResourcesVpcConfig; v != nil {
		cluster.ResourcesVpcConfig = &kubernetes.VPCConfig{
			VPCID: awssdk.ToString(v.VpcId), EndpointPublicAccess: v.EndpointPublicAccess, EndpointPrivateAccess: v.EndpointPrivateAccess, PublicAccessCidrs: v.PublicAccessCidrs,
		}
	}
	if a := c.AccessConfig; a != nil {
		cluster.AccessConfig = &kubernetes.AccessConfig{AuthenticationMode: string(a.AuthenticationMode)}
	}
	return kubernetes.DescribeClusterOutput{Cluster: cluster}, nil
}

// kubernetesPorts are a region's EKS clusters and their API, read with the
// account's credentials (discovery's kubernetesPorts); reader names who reads
// them in the reason of a cluster that refuses it.
func kubernetesPorts(port eksPort, creds awssdk.CredentialsProvider, region, reader string, now func() time.Time) *kubernetes.Ports {
	return &kubernetes.Ports{
		EKS:    port,
		Reader: reader,
		Of: func(ctx context.Context, cluster string) (kubernetes.Client, error) {
			view, err := kubernetes.ClusterOf(ctx, port, cluster, now())
			if err != nil {
				return nil, err
			}
			if creds == nil {
				return nil, errors.New("no credentials to sign an EKS token with")
			}
			c, err := creds.Retrieve(ctx)
			if err != nil {
				return nil, err
			}
			return kubernetes.New(view.Endpoint, view.CA, kubernetes.Token(cluster, region, c, now())), nil
		},
	}
}
