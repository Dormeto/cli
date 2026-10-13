package discover

// How idle a target group was over the last 7 days.
//
// Ports manager packages/discovery/src/scan.ts (idleHours, lbDimension,
// tgDimension).

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

const idleWindowHours = 168

// lbDimension is the CloudWatch LoadBalancer dimension of a load balancer ARN.
func lbDimension(arn string) string {
	if _, after, ok := strings.Cut(arn, ":loadbalancer/"); ok {
		// split(':loadbalancer/')[1] stops at a second occurrence
		after, _, _ = strings.Cut(after, ":loadbalancer/")
		return after
	}
	return arn
}

// tgDimension is the CloudWatch TargetGroup dimension of a target group ARN.
func tgDimension(arn string) string {
	return arn[strings.LastIndex(arn, ":")+1:]
}

type pair struct{ lb, tg string }

type idle struct{ hours, requests int }

// idleHours reads the hourly request sums of the last 7 days per target group:
// idle hours are hours without data or with 0 requests.
func idleHours(ctx context.Context, cw CloudWatchPort, pairs []pair, now time.Time) (map[string]idle, error) {
	end := now.UTC().Truncate(time.Hour)
	start := end.Add(-idleWindowHours * time.Hour)
	out := map[string]idle{}
	for _, batch := range chunks(pairs, 500) {
		queries := make([]MetricDataQuery, len(batch))
		for i, p := range batch {
			queries[i] = MetricDataQuery{
				ID:         "q" + strconv.Itoa(i),
				ReturnData: true,
				MetricStat: MetricStat{
					Metric: Metric{
						Namespace:  "AWS/ApplicationELB",
						MetricName: "RequestCount",
						Dimensions: []Dimension{{"LoadBalancer", lbDimension(p.lb)}, {"TargetGroup", tgDimension(p.tg)}},
					},
					Period: 3600,
					Stat:   "Sum",
				},
			}
		}
		res, err := cw.GetMetricData(ctx, GetMetricDataInput{StartTime: start, EndTime: end, MetricDataQueries: queries})
		if err != nil {
			return nil, err
		}
		for _, r := range res.MetricDataResults {
			i, err := strconv.Atoi(strings.TrimPrefix(r.ID, "q"))
			if r.ID == "" || err != nil || i < 0 || i >= len(batch) {
				continue
			}
			active, sum := 0, 0.0
			for _, v := range r.Values {
				if v > 0 {
					active++
				}
				sum += v
			}
			out[batch[i].tg] = idle{idleWindowHours - active, int(math.Floor(sum + 0.5))}
		}
	}
	return out, nil
}

func chunks[T any](list []T, size int) [][]T {
	var out [][]T
	for i := 0; i < len(list); i += size {
		out = append(out, list[i:min(i+size, len(list))])
	}
	return out
}
