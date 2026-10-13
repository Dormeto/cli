package kubernetes

// The ports discovery and the estimate read EKS clusters through: the EKS API's
// own shapes (camelCase JSON), and the client of a cluster by name. Ports
// manager packages/discovery/src/kubernetes.ts (KubeClient, KubernetesPorts).

import "context"

// ListClustersInput is EKS ListClusters' input (a page).
type ListClustersInput struct {
	NextToken string `json:"nextToken,omitempty"`
}

// ListClustersOutput is its answer.
type ListClustersOutput struct {
	Clusters  []string `json:"clusters,omitempty"`
	NextToken string   `json:"nextToken,omitempty"`
}

// DescribeClusterInput is EKS DescribeCluster's input.
type DescribeClusterInput struct {
	Name string `json:"name"`
}

// DescribeClusterOutput is its answer.
type DescribeClusterOutput struct {
	Cluster *EKSCluster `json:"cluster,omitempty"`
}

// EKSCluster is the part of an EKS cluster the estimate reads.
type EKSCluster struct {
	Name                 string                `json:"name,omitempty"`
	Version              string                `json:"version,omitempty"`
	Endpoint             string                `json:"endpoint,omitempty"`
	CertificateAuthority *CertificateAuthority `json:"certificateAuthority,omitempty"`
	ResourcesVpcConfig   *VPCConfig            `json:"resourcesVpcConfig,omitempty"`
	AccessConfig         *AccessConfig         `json:"accessConfig,omitempty"`
}

// CertificateAuthority holds the cluster's CA, PEM in base64.
type CertificateAuthority struct {
	Data string `json:"data,omitempty"`
}

// VPCConfig is the cluster's VPC and endpoint access.
type VPCConfig struct {
	VPCID                 string   `json:"vpcId,omitempty"`
	EndpointPublicAccess  bool     `json:"endpointPublicAccess,omitempty"`
	EndpointPrivateAccess bool     `json:"endpointPrivateAccess,omitempty"`
	PublicAccessCidrs     []string `json:"publicAccessCidrs,omitempty"`
}

// AccessConfig is the cluster's authentication mode.
type AccessConfig struct {
	AuthenticationMode string `json:"authenticationMode,omitempty"`
}

// EKSPort is the EKS API (ListClusters, DescribeCluster).
type EKSPort interface {
	ListClusters(ctx context.Context, in ListClustersInput) (ListClustersOutput, error)
	DescribeCluster(ctx context.Context, in DescribeClusterInput) (DescribeClusterOutput, error)
}

// Ports are what discovery reads clusters through. Of gives the client of a
// cluster by name (for real: its endpoint and certificate authority from
// DescribeCluster, an EKS token). Reader names who reads the clusters, in the
// reason of one refused (DmtDiscoveryRole when empty).
type Ports struct {
	EKS    EKSPort
	Of     func(ctx context.Context, cluster string) (Client, error)
	Reader string
}
