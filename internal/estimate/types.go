// Package estimate is phase 3 of the estimate (vision docs/specs/2026-10-10-savings-estimate-design.md §6): each
// eligible workload's requests replayed by the product's idle window advisor (R10) and priced by its price book, at the
// report's window (--window, an hour by default) and at the recommended one; what cannot be estimated, with the reason;
// the totals by account, region and all, net of Dormeto's cost. Conservative where the facts are unknown before
// install (decision 5).
//
// This file ports the types of manager packages/estimate/src/estimate.ts (WorkloadEstimate, Companion, LeftOutCode,
// DatabaseLeftOut, LeftOut, Busy, Small, Kinds, Totals, CostPart, DormetoCost, Estimate). Each marshals to the JSON
// Node writes: the same names, in the same order, fields Node leaves undefined absent.
package estimate

import (
	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/options"
)

// Uptime is a measured uptime: the hours it ran a day, or why it is unknown (then all the time). One of the two is
// set.
type Uptime struct {
	HoursPerDay *float64 `json:"hoursPerDay,omitempty"`
	Unreadable  *string  `json:"unreadable,omitempty"`
}

// AtWindow is a workload's figures at the report's window (options.Window).
type AtWindow struct {
	USDMonth    float64 `json:"usdMonth"`
	WakesPerDay float64 `json:"wakesPerDay"`
	AsleepHours float64 `json:"asleepHours"`
}

// Recommended is a workload's figures at its recommended window.
type Recommended struct {
	Minutes     int     `json:"minutes"`
	USDMonth    float64 `json:"usdMonth"`
	WakesPerDay float64 `json:"wakesPerDay"`
}

// WorkloadEstimate is a workload estimated.
type WorkloadEstimate struct {
	Workload discover.Workload `json:"workload"`
	// No request in 14 days.
	Quiet bool `json:"quiet"`
	// What its naps would stop, running every hour of the baseline (or, measured, the hours it ran): its cost a month
	// now.
	ActualMonth float64 `json:"actualMonth"`
	// An EC2 instance without an Auto Scaling group (--ec2-uptime) or an ECS service (--ecs-uptime): the hours it ran a
	// day, or why they are unknown (then 24/7).
	Uptime      *Uptime     `json:"uptime,omitempty"`
	AtWindow    AtWindow    `json:"atWindow"`
	Recommended Recommended `json:"recommended"`
	// Its companion databases, napped with it: their saving (included in the workload's) at the report's window and at
	// its recommended one.
	Databases []Companion `json:"databases,omitempty"`
}

// Companion is a companion database napped with its workload. Uptime: measured (--rds-uptime), the hours it was
// available a day, or why unknown (then all the time).
type Companion struct {
	Kind                string  `json:"kind"`
	ID                  string  `json:"id"`
	USDMonth            float64 `json:"usdMonth"`
	RecommendedUSDMonth float64 `json:"recommendedUsdMonth"`
	Uptime              *Uptime `json:"uptime,omitempty"`
}

// LeftOutCode is why a companion database is left out, in a word (the summary counts them).
type LeftOutCode string

// The codes of a companion database left out.
const (
	CodeShared    LeftOutCode = "shared"
	CodeTags      LeftOutCode = "tags"
	CodeStoppable LeftOutCode = "stoppable"
	CodeWakes     LeftOutCode = "wakes"
	CodeStopped   LeftOutCode = "stopped"
	CodeIdle      LeftOutCode = "idle"
	CodePrice     LeftOutCode = "price"
)

// DatabaseRef names a database: its kind and identifier.
type DatabaseRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// DatabaseLeftOut is a companion database left out, with its workload and why.
type DatabaseLeftOut struct {
	Workload discover.Workload `json:"workload"`
	Database DatabaseRef       `json:"database"`
	Reason   string            `json:"reason"`
	Code     LeftOutCode       `json:"code"`
}

// LeftOut is a workload that cannot be estimated, and why.
type LeftOut struct {
	Workload discover.Workload `json:"workload"`
	Reason   string            `json:"reason"`
}

// Busy is a workload left out at its recommended window (Minutes) for waking more than --max-wakes a day, with what
// it would add.
type Busy struct {
	Workload    discover.Workload `json:"workload"`
	WakesPerDay float64           `json:"wakesPerDay"`
	USDMonth    float64           `json:"usdMonth"`
	Minutes     int               `json:"minutes"`
}

// Small is a workload left out at its recommended window (Minutes) for saving less than --min-saving a month, with
// what it would add.
type Small struct {
	Workload discover.Workload `json:"workload"`
	USDMonth float64           `json:"usdMonth"`
	Minutes  int               `json:"minutes"`
}

// Kinds are the workloads by backend: an Auto Scaling group, an EC2 instance, an ECS service, a Kubernetes owner on
// EKS.
type Kinds struct {
	ASG int `json:"asg"`
	EC2 int `json:"ec2"`
	ECS int `json:"ecs"`
	EKS int `json:"eks"`
}

// Totals are the figures of some workloads (all, an account's, a region's). Excluded: the workloads not estimated,
// whatever the reason (tags, left out, too many wakes, too small a saving). WithDormetoMonth: the actual cost less the
// savings at the report's window, plus Dormeto's cost (an account's: its routers and workloads; the total's: the
// manager too).
type Totals struct {
	Workloads           int     `json:"workloads"`
	Kinds               Kinds   `json:"kinds"`
	Excluded            int     `json:"excluded"`
	Databases           int     `json:"databases"`
	ActualMonth         float64 `json:"actualMonth"`
	USDMonthWindow      float64 `json:"usdMonthWindow"`
	USDMonthRecommended float64 `json:"usdMonthRecommended"`
	DormetoMonth        float64 `json:"dormetoMonth"`
	WithDormetoMonth    float64 `json:"withDormetoMonth"`
}

// CostPart is a part of Dormeto's cost: how many, the cost of one a month, theirs.
type CostPart struct {
	Count     int     `json:"count"`
	UnitMonth float64 `json:"unitMonth"`
	USDMonth  float64 `json:"usdMonth"`
}

// Where is an account and region.
type Where struct {
	Account string `json:"account"`
	Region  string `json:"region"`
}

// Routers are Dormeto's routers: a part of its cost, and where they run.
type Routers struct {
	CostPart
	Where []Where `json:"where"`
}

// DormetoCost is Dormeto's cost by part.
type DormetoCost struct {
	Manager   CostPart `json:"manager"`
	Routers   Routers  `json:"routers"`
	Workloads CostPart `json:"workloads"`
	USDMonth  float64  `json:"usdMonth"`
}

// DatabasesUnread is a region whose databases could not be read.
type DatabasesUnread struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Reason  string `json:"reason"`
}

// IneligibleAt is a rule that is not a workload, with its account and region.
type IneligibleAt struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	discover.IneligibleRule
}

// FailedRegion is a region whose discovery failed.
type FailedRegion struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Error   string `json:"error"`
}

// UnreadableCluster is an EKS cluster the profile cannot read (R10.5): its workloads are not estimated.
type UnreadableCluster struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// Estimate is the estimate the reports are written from. Slices are never nil (written as []).
type Estimate struct {
	GeneratedAt  string  `json:"generatedAt"`
	Organization *string `json:"organization"`
	Why          *string `json:"why"`
	// The options, without out and profiles (their JSON leaves them out).
	Options  options.Options `json:"options"`
	Accounts int             `json:"accounts"`
	// The accounts reached before --accounts restricted them.
	AccountsListed int                `json:"accountsListed"`
	AccountNames   Ordered[string]    `json:"accountNames"`
	Totals         Totals             `json:"totals"`
	Dormeto        DormetoCost        `json:"dormeto"`
	ByAccount      Ordered[Totals]    `json:"byAccount"`
	ByRegion       Ordered[Totals]    `json:"byRegion"`
	Workloads      []WorkloadEstimate `json:"workloads"`
	LeftOut        []LeftOut          `json:"leftOut"`
	// Excluded by their tags (--exclude-tags), before their requests were read.
	Excluded []Excluded `json:"excluded"`
	// Left out at their recommended window: waking more than options.MaxWakes a day, saving less than
	// options.MinSaving.
	Busy  []Busy  `json:"busy"`
	Small []Small `json:"small"`
	// Companion databases left out, with why; the regions whose databases could not be read.
	DatabasesLeftOut []DatabaseLeftOut      `json:"databasesLeftOut"`
	DatabasesUnread  []DatabasesUnread      `json:"databasesUnread"`
	Ineligible       []IneligibleAt         `json:"ineligible"`
	Unreachable      []accounts.Unreachable `json:"unreachable"`
	FailedRegions    []FailedRegion         `json:"failedRegions"`
	// The EKS clusters the profile cannot read (R10.5): their workloads are not estimated.
	UnreadableClusters []UnreadableCluster `json:"unreadableClusters"`
	Assumptions        []string            `json:"assumptions"`
}
