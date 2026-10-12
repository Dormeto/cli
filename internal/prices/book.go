// The price book: AWS's on-demand list prices of what a nap stops, from the
// Price List API, cached per region and item, and Spot prices and discounts.
// Ports createPriceBook of fleet/src/prices.ts.
package prices

import (
	"context"
	"sync"
	"time"

	"github.com/Dormeto/cli/internal/stopped"
)

// Discounts are percentages off the list prices for commitments: compute (EC2,
// Fargate), databases (RDS, Aurora). Never off Spot.
type Discounts struct {
	Compute   float64 `json:"compute"`
	Databases float64 `json:"databases"`
}

// FargateSpotShare is AWS's largest announced Fargate Spot discount (up to 70%
// off), as AWS publishes no Fargate Spot price: the smallest saving.
const FargateSpotShare = 0.3

const (
	spotDays   = 1
	listDays   = 30
	memoryFor  = time.Hour       // in memory: a price an hour
	failureFor = 5 * time.Minute // a failure five minutes
)

var engines = map[string]string{
	"postgres": "PostgreSQL", "mysql": "MySQL", "mariadb": "MariaDB",
	"aurora-postgresql": "Aurora PostgreSQL", "aurora-mysql": "Aurora MySQL", "aurora": "Aurora MySQL",
}

func supportedOS(os string) bool { return os == "Linux" || os == "Windows" }

func term(field, value string) Filter      { return Filter{"TERM_MATCH", field, value} }
func contains_(field, value string) Filter { return Filter{"CONTAINS", field, value} }

func off(usd *float64, percent float64) *float64 {
	if usd == nil {
		return nil
	}
	v := *usd * (1 - percent/100)
	return &v
}

type remembered struct {
	usd   *float64
	until time.Time
}

type inflight struct {
	done chan struct{}
	usd  *float64
	err  error
}

// Book prices the items a nap stops.
type Book struct {
	pricing PricingPort
	spot    SpotPort
	cache   Cache
	now     func() time.Time

	mu      sync.Mutex
	memory  map[string]remembered
	pending map[string]*inflight
}

// NewBook makes a price book over the ports. spot may be nil (no Spot prices);
// now nil is the clock.
func NewBook(pricing PricingPort, spot SpotPort, cache Cache, now func() time.Time) *Book {
	if now == nil {
		now = time.Now
	}
	return &Book{pricing: pricing, spot: spot, cache: cache, now: now, memory: map[string]remembered{}, pending: map[string]*inflight{}}
}

// lookup is one price: from the table, else the API; a failed call prices
// nothing, caches nothing in the table, and is not asked again for five minutes.
func (b *Book) lookup(key string, ask func() (*float64, error), days int) (*float64, error) {
	cached, err := b.cache.GetPrice(key)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		b.remember(key, cached.USD, memoryFor)
		return cached.USD, nil
	}
	usd, err := ask()
	if err != nil {
		b.remember(key, nil, failureFor)
		return nil, nil
	}
	if err := b.cache.PutPrice(key, usd, days); err != nil {
		return nil, err
	}
	b.remember(key, usd, memoryFor)
	return usd, nil
}

func (b *Book) remember(key string, usd *float64, d time.Duration) {
	b.mu.Lock()
	b.memory[key] = remembered{usd, b.now().Add(d)}
	b.mu.Unlock()
}

// cachedPrice reads memory, shares a price being asked, else looks it up.
func (b *Book) cachedPrice(key string, ask func() (*float64, error), days int) (*float64, error) {
	b.mu.Lock()
	if known, ok := b.memory[key]; ok && known.until.After(b.now()) {
		b.mu.Unlock()
		return known.usd, nil
	}
	if p, ok := b.pending[key]; ok {
		b.mu.Unlock()
		<-p.done
		return p.usd, p.err
	}
	p := &inflight{done: make(chan struct{})}
	b.pending[key] = p
	b.mu.Unlock()

	p.usd, p.err = b.lookup(key, ask, days)
	b.mu.Lock()
	delete(b.pending, key)
	b.mu.Unlock()
	close(p.done)
	return p.usd, p.err
}

func (b *Book) price(ctx context.Context, key, service string, filters []Filter) (*float64, error) {
	return b.cachedPrice(key, func() (*float64, error) {
		out, err := b.pricing.GetProducts(ctx, GetProductsInput{ServiceCode: service, Filters: filters, MaxResults: 10})
		if err != nil {
			return nil, err
		}
		return onDemand(out.PriceList)
	}, listDays)
}

// spotPrice is a Spot price, from the manager's account in the item's region
// (one it has not enabled fails: not priced).
func (b *Book) spotPrice(ctx context.Context, region, typ, os string) (*float64, error) {
	if b.spot == nil {
		return nil, nil
	}
	return b.cachedPrice(region+"#ec2-spot#"+typ+"#"+os, func() (*float64, error) {
		desc := "Linux/UNIX"
		if os == "Windows" {
			desc = "Windows"
		}
		out, err := b.spot.History(ctx, region, SpotHistoryInput{InstanceTypes: []string{typ}, ProductDescriptions: []string{desc}, StartTime: b.now()})
		if err != nil {
			return nil, err
		}
		return spotAverage(out.SpotPriceHistory), nil
	}, spotDays)
}

// instanceHourly is an EC2 instance's net hourly price: Spot, or on-demand less
// the compute discount; nil when it cannot be priced.
func (b *Book) instanceHourly(ctx context.Context, region, typ, os string, spot bool, d Discounts) (*float64, error) {
	if !supportedOS(os) {
		return nil, nil
	}
	if spot {
		return b.spotPrice(ctx, region, typ, os)
	}
	usd, err := b.price(ctx, region+"#ec2#"+typ+"#"+os, "AmazonEC2", []Filter{
		term("instanceType", typ), term("regionCode", region), term("locationType", "AWS Region"), term("operatingSystem", os), term("tenancy", "Shared"),
		term("preInstalledSw", "NA"), term("capacitystatus", "Used"), term("licenseModel", "No License required"),
	})
	if err != nil {
		return nil, err
	}
	return off(usd, d.Compute), nil
}

// Hourly is the USD an hour of an item, in its region, net: a Spot price, or the
// list price less its family's discount; nil when it cannot be priced. An error
// is the price cache's (a failed API call prices nothing instead).
func (b *Book) Hourly(ctx context.Context, region string, item stopped.Item, d Discounts) (*float64, error) {
	switch item.Kind {
	case stopped.KindEC2:
		usd, err := b.instanceHourly(ctx, region, item.Type, item.OS, item.Spot, d)
		if err != nil || usd == nil {
			return nil, err
		}
		v := *usd * float64(item.Count)
		return &v, nil
	case stopped.KindK8sPods:
		// Nothing saved where no autoscaler removes the nodes the pods free: the nodes stay.
		if !item.Autoscaled {
			zero := 0.0
			return &zero, nil
		}
		var alloc stopped.Allocatable
		if item.Allocatable != nil {
			alloc = *item.Allocatable
		}
		if item.NodeType == nil || *item.NodeType == "" || !(alloc.CPU > 0) || !(alloc.MemoryGb > 0) {
			return nil, nil
		}
		// Whichever runs out first decides how pods fit: the larger of the CPU and memory shares, at most the node.
		share := min(1, max(item.CPU/alloc.CPU, item.MemoryGb/alloc.MemoryGb))
		node, err := b.instanceHourly(ctx, region, *item.NodeType, item.OS, item.Spot, d)
		if err != nil || node == nil {
			return nil, err
		}
		v := float64(float64(*node*share) * float64(item.Count))
		return &v, nil
	case stopped.KindFargate:
		if !supportedOS(item.OS) {
			return nil, nil
		}
		prefix := "Fargate-"
		if item.OS == "Windows" {
			prefix = "Fargate-Windows-"
		} else if item.Arch == "arm64" {
			prefix = "Fargate-ARM-"
		}
		vcpu, err := b.price(ctx, region+"#fargate#"+prefix+"vcpu", "AmazonECS", []Filter{term("regionCode", region), contains_("usagetype", prefix+"vCPU-Hours:perCPU")})
		if err != nil {
			return nil, err
		}
		gb, err := b.price(ctx, region+"#fargate#"+prefix+"gb", "AmazonECS", []Filter{term("regionCode", region), contains_("usagetype", prefix+"GB-Hours")})
		if err != nil {
			return nil, err
		}
		if vcpu == nil || gb == nil {
			return nil, nil
		}
		usd := float64(float64(*vcpu*item.VCPU)+float64(*gb*item.MemoryGb)) * float64(item.Count)
		if item.Spot {
			v := usd * FargateSpotShare
			return &v, nil
		}
		return off(&usd, d.Compute), nil
	case stopped.KindRDS:
		engine, ok := engines[item.Engine]
		if !ok {
			return nil, nil
		}
		deployment, usage := "Single-AZ", "InstanceUsage:"
		if item.MultiAZ {
			deployment, usage = "Multi-AZ", "Multi-AZUsage:"
		}
		usd, err := b.price(ctx, region+"#rds#"+item.Class+"#"+engine+"#"+deployment, "AmazonRDS", []Filter{
			term("instanceType", item.Class), term("regionCode", region), term("locationType", "AWS Region"), term("databaseEngine", engine), term("deploymentOption", deployment),
			// Standard storage, not I/O-Optimized (InstanceUsageIOOptimized:), whose instances cost about 30% more.
			contains_("usagetype", usage),
		})
		if err != nil {
			return nil, err
		}
		return off(usd, d.Databases), nil
	case stopped.KindAuroraServerless:
		engine, ok := engines[item.Engine]
		if !ok {
			return nil, nil
		}
		usd, err := b.price(ctx, region+"#aurora-serverless#"+engine, "AmazonRDS", []Filter{term("regionCode", region), term("databaseEngine", engine), contains_("usagetype", "Aurora:ServerlessV2Usage")})
		if err != nil || usd == nil {
			return nil, err
		}
		v := *usd * item.ACU
		return off(&v, d.Databases), nil
	}
	return nil, nil
}
