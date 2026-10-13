// Package aws holds the adapters of the estimate's ports over the AWS SDK for
// Go v2: a profile's shared configuration (SSO sessions included), the
// accounts it reaches (STS, Organizations), an account's clients in a region
// (the scan's, what a nap stops, the EKS clusters and their API), the Price List
// and Spot price history. Read-only: Describe, List and Get calls, and
// sts:AssumeRole (readonly_test.go). The adapters map the SDK's types to the
// ports' AWS JSON shapes field by field, and return the SDK's errors as they
// are (awserr reads them).
//
// This file: the shared configuration, an account's credentials, the clients'
// retries and an account's clients in a region. Ports manager
// packages/estimate/src/bin.ts (awsOf: fromIni, the profile's credentials or a
// role's per account, portsOf) and packages/discovery/src/aws.ts
// (regionalClients).
package aws

import (
	"context"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/kubernetes"
	"github.com/Dormeto/cli/internal/stopped"
)

// global is the region of the calls the Node estimate makes in us-east-1:
// STS, Organizations, the Price List and the enabled regions.
const global = "us-east-1"

// attempts is how many times a regional client tries a call (Node's
// maxAttempts: 5); the global clients keep the SDK's default (3).
const attempts = 5

// Load is a profile's shared configuration (~/.aws/config and credentials, or
// AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE; SSO sessions included). Its
// credentials are resolved when a call needs them, refreshed by the SDK. A
// profile missing from the config is an error carrying the SDK's message; an
// expired SSO session fails the first call with the SDK's message ("the SSO
// session has expired or is invalid", "cached SSO token is expired"), before
// anything is sent.
func Load(ctx context.Context, profile string) (awssdk.Config, error) {
	return config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile))
}

// Of is an account's configuration: the profile's own (creds nil: the
// profile's own account, its credentials refreshed by the SDK) or with a role's
// credentials, as assumed (accounts.ReachedAccount's Credentials).
func Of(cfg awssdk.Config, creds *awssdk.Credentials) awssdk.Config {
	if creds == nil {
		return cfg
	}
	cfg = cfg.Copy()
	cfg.Credentials = credentials.StaticCredentialsProvider{Value: *creds}
	return cfg
}

// standard is the SDK's standard retryer trying a call n times.
func standard(n int) awssdk.Retryer {
	return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = n })
}

// adaptive is the SDK's adaptive retryer (the standard one, and a client-side
// rate limit that slows down a throttled client), as the routers' ELB clients,
// trying a call n times.
func adaptive(n int) awssdk.Retryer {
	return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
		o.StandardOptions = append(o.StandardOptions, func(s *retry.StandardOptions) { s.MaxAttempts = n })
	})
}

// Region is an account's clients in a region (discovery's regionalClients and
// the estimate's portsOf): ELB in adaptive retry mode; Auto Scaling, EC2, ECS,
// CloudWatch, EKS and RDS with the standard retryer; 5 attempts each, whatever
// the user's max_attempts or retry_mode (Node's explicit maxAttempts).
type Region struct {
	elbv2       elbv2Port
	autoscaling autoScalingPort
	ec2         interface {
		ec2API
		regionEC2API
	}
	ecs        ecsAPI
	cloudwatch cloudWatchPort
	rds        rdsPort
	eks        eksPort
	kubernetes *kubernetes.Ports
}

// NewRegion makes an account's clients in a region (cfg: the account's, from
// Of). reader names who reads the EKS clusters, in the reason of one it cannot
// read (the estimate: "the profile").
func NewRegion(cfg awssdk.Config, region, reader string) *Region {
	r := &Region{
		elbv2: elbv2Port{elasticloadbalancingv2.NewFromConfig(cfg, func(o *elasticloadbalancingv2.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, adaptive(attempts), awssdk.RetryModeAdaptive, 0
		})},
		autoscaling: autoScalingPort{autoscaling.NewFromConfig(cfg, func(o *autoscaling.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		})},
		ec2: ec2.NewFromConfig(cfg, func(o *ec2.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		ecs: ecs.NewFromConfig(cfg, func(o *ecs.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		cloudwatch: cloudWatchPort{cloudwatch.NewFromConfig(cfg, func(o *cloudwatch.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		})},
		rds: rdsPort{rds.NewFromConfig(cfg, func(o *rds.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		})},
		eks: eksPort{eks.NewFromConfig(cfg, func(o *eks.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		})},
	}
	r.kubernetes = kubernetesPorts(r.eks, cfg.Credentials, region, reader, time.Now)
	return r
}

// Scan is the ports of the region's scan (discover.ScanRegion): ELB, Auto
// Scaling, EC2, ECS, CloudWatch and the EKS clusters.
func (r *Region) Scan() discover.ScanPorts {
	return discover.ScanPorts{
		ELBv2: r.elbv2, AutoScaling: r.autoscaling, EC2: scanEC2{r.ec2}, ECS: scanECS{r.ecs}, CloudWatch: r.cloudwatch, Kubernetes: r.kubernetes,
	}
}

// Stopped is the ports of what a nap stops in the region (stopped.Describe):
// EC2, ECS, RDS and the clusters' clients, the scan's.
func (r *Region) Stopped() stopped.Ports {
	return stopped.Ports{EC2: stoppedEC2{r.ec2}, ECS: stoppedECS{r.ecs}, RDS: r.rds, KubernetesOf: r.kubernetes.Of}
}
