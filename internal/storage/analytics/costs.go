package analytics

import (
	"context"
	"sort"
	"time"
)

// CostGroup selects the dimension a cost report is bucketed by.
type CostGroup string

const (
	// GroupTenant buckets by tenant name.
	GroupTenant CostGroup = "tenant"
	// GroupModel buckets by public model name.
	GroupModel CostGroup = "model"
	// GroupKey buckets by credential reference.
	GroupKey CostGroup = "key"
)

// ParseCostGroup maps a query parameter onto a group, defaulting to tenant for
// an empty or unrecognised value. A report is never refused for a bad group:
// the caller asked for costs, and tenant is the most useful default.
func ParseCostGroup(raw string) CostGroup {
	switch CostGroup(raw) {
	case GroupModel:
		return GroupModel
	case GroupKey:
		return GroupKey
	default:
		return GroupTenant
	}
}

// CostBucket is one aggregated row: the totals of every request that fell into
// the bucket, plus the cache split so a dashboard can show what the prompt
// cache actually saved.
type CostBucket struct {
	Key        string  `json:"key"`
	Requests   int64   `json:"requests"`
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CachedRead int64   `json:"cached_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	CostMicros int64   `json:"cost_micros"`
	CostUSD    float64 `json:"cost_usd"`
	ErrorCount int64   `json:"error_count"`
}

// CostSeries is one bucket on one UTC day.
type CostSeries struct {
	Day        string  `json:"day"`
	Key        string  `json:"key"`
	Requests   int64   `json:"requests"`
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CachedRead int64   `json:"cached_read_tokens"`
	CacheWrite int64   `json:"cache_write_tokens"`
	CostMicros int64   `json:"cost_micros"`
	CostUSD    float64 `json:"cost_usd"`
	ErrorCount int64   `json:"error_count"`
}

// CostReport is the aggregated answer for one query.
type CostReport struct {
	GroupBy CostGroup    `json:"group_by"`
	Since   int64        `json:"since"`
	Until   int64        `json:"until"`
	Total   CostBucket   `json:"total"`
	Buckets []CostBucket `json:"buckets"`
	Series  []CostSeries `json:"series"`
}

// microsPerUSD converts integer micro-USD into the float dollars the API
// speaks. It lives here so every field of the report uses one conversion.
const microsPerUSD = 1_000_000.0

func microsToUSD(micros int64) float64 { return float64(micros) / microsPerUSD }

// costCounters is the mutable accumulator behind a bucket or a series cell.
type costCounters struct {
	reqs       int64
	tokIn      int64
	tokOut     int64
	cachedRead int64
	cacheWrite int64
	costMicros int64
	errors     int64
}

// add folds one request log into the counters.
func (c *costCounters) add(log RequestLog) {
	c.reqs++
	c.tokIn += int64(log.TokensIn)
	c.tokOut += int64(log.TokensOut)
	c.cachedRead += int64(log.CachedReadTokens)
	c.cacheWrite += int64(log.CacheWriteTokens)
	c.costMicros += log.CostMicros
	if log.Status >= 400 {
		c.errors++
	}
}

// bucket renders the counters as an API row.
func (c costCounters) bucket(key string) CostBucket {
	return CostBucket{
		Key:        key,
		Requests:   c.reqs,
		TokensIn:   c.tokIn,
		TokensOut:  c.tokOut,
		CachedRead: c.cachedRead,
		CacheWrite: c.cacheWrite,
		CostMicros: c.costMicros,
		CostUSD:    microsToUSD(c.costMicros),
		ErrorCount: c.errors,
	}
}

// bucketKey extracts the grouping dimension from a log. An empty value becomes
// "(unknown)" so a request with no tenant still shows up in the report rather
// than silently vanishing into a blank row.
func bucketKey(log RequestLog, groupBy CostGroup) string {
	var key string
	switch groupBy {
	case GroupModel:
		key = log.Model
	case GroupKey:
		key = log.KeyRef
	default:
		key = log.Tenant
	}
	if key == "" {
		return "(unknown)"
	}
	return key
}

// dayKey renders a millisecond timestamp as a UTC date string.
func dayKey(tsMs int64) string {
	if tsMs <= 0 {
		return "(unknown)"
	}
	return time.UnixMilli(tsMs).UTC().Format("2006-01-02")
}

// AggregateCosts folds request logs into a grouped, per-day report. Logs
// outside [since, until] are ignored; a zero bound means unbounded on that
// side. The result is deterministic — buckets sort by descending cost then key,
// series by day then key — so two runs over the same logs render identically.
func AggregateCosts(logs []RequestLog, groupBy CostGroup, since, until int64) CostReport {
	report := CostReport{GroupBy: groupBy, Since: since, Until: until}

	buckets := make(map[string]*costCounters)
	series := make(map[string]*costCounters)
	seriesKey := make(map[string]string)
	var total costCounters

	for _, log := range logs {
		if since > 0 && log.Timestamp < since {
			continue
		}
		if until > 0 && log.Timestamp > until {
			continue
		}

		key := bucketKey(log, groupBy)
		b, ok := buckets[key]
		if !ok {
			b = &costCounters{}
			buckets[key] = b
		}
		b.add(log)

		day := dayKey(log.Timestamp)
		sk := day + "\x00" + key
		s, ok := series[sk]
		if !ok {
			s = &costCounters{}
			series[sk] = s
			seriesKey[sk] = key
		}
		s.add(log)

		total.add(log)
	}

	report.Total = total.bucket("total")

	report.Buckets = make([]CostBucket, 0, len(buckets))
	for key, b := range buckets {
		report.Buckets = append(report.Buckets, b.bucket(key))
	}
	sort.Slice(report.Buckets, func(i, j int) bool {
		if report.Buckets[i].CostMicros != report.Buckets[j].CostMicros {
			return report.Buckets[i].CostMicros > report.Buckets[j].CostMicros
		}
		return report.Buckets[i].Key < report.Buckets[j].Key
	})

	report.Series = make([]CostSeries, 0, len(series))
	for sk, s := range series {
		day := sk[:len(sk)-len(seriesKey[sk])-1]
		report.Series = append(report.Series, CostSeries{
			Day:        day,
			Key:        seriesKey[sk],
			Requests:   s.reqs,
			TokensIn:   s.tokIn,
			TokensOut:  s.tokOut,
			CachedRead: s.cachedRead,
			CacheWrite: s.cacheWrite,
			CostMicros: s.costMicros,
			CostUSD:    microsToUSD(s.costMicros),
			ErrorCount: s.errors,
		})
	}
	sort.Slice(report.Series, func(i, j int) bool {
		if report.Series[i].Day != report.Series[j].Day {
			return report.Series[i].Day < report.Series[j].Day
		}
		return report.Series[i].Key < report.Series[j].Key
	})

	return report
}

// CostReport aggregates the stored history. It reuses the same History read
// path, so the report is always consistent with what the dashboard already
// shows and needs no second persistence layer.
func (s *Store) CostReport(ctx context.Context, groupBy CostGroup, since, until int64) (CostReport, error) {
	if err := ctx.Err(); err != nil {
		return CostReport{}, err
	}
	s.mu.RLock()
	logs := make([]RequestLog, len(s.history))
	copy(logs, s.history)
	s.mu.RUnlock()

	return AggregateCosts(logs, groupBy, since, until), nil
}
