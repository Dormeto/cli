package aws

// ReadOnlyOperations are the operations the adapters call, as <service>.<Operation>: the list the read-only test checks
// the sources against, and the README's permissions are checked against.
var ReadOnlyOperations = []string{
	"autoscaling.DescribeAutoScalingGroups",
	"cloudwatch.GetMetricData",
	"ec2.DescribeInstances",
	"ec2.DescribeRegions",
	"ec2.DescribeSecurityGroups",
	"ec2.DescribeSpotPriceHistory",
	"ecs.DescribeServices",
	"ecs.DescribeTaskDefinition",
	"ecs.ListClusters",
	"ecs.ListServices",
	"eks.DescribeCluster",
	"eks.ListClusters",
	"elasticloadbalancingv2.DescribeListeners",
	"elasticloadbalancingv2.DescribeLoadBalancers",
	"elasticloadbalancingv2.DescribeRules",
	"elasticloadbalancingv2.DescribeTags",
	"elasticloadbalancingv2.DescribeTargetGroups",
	"elasticloadbalancingv2.DescribeTargetHealth",
	"organizations.DescribeOrganization",
	"organizations.ListAccountsForParent",
	"organizations.ListOrganizationalUnitsForParent",
	"organizations.ListRoots",
	"organizations.ListTagsForResource",
	"pricing.GetProducts",
	"rds.DescribeDBClusters",
	"rds.DescribeDBInstances",
	"sts.AssumeRole",
	"sts.GetCallerIdentity",
}
