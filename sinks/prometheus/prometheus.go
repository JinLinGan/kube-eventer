package prometheus

import (
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/AliyunContainerService/kube-eventer/core"
	"github.com/prometheus/client_golang/prometheus"
)

type typeRule struct {
	EventReason    string
	MessageRegex   *regexp.Regexp
	PlatformReason string
}

const UnknownPlatformReason = "Unknown"

const DefaultDeleteCacheInterval = time.Second * 5

const DefaultCacheTTL = time.Minute * 15

var DefaultRules = []typeRule{
	{
		EventReason:    "Failed",
		MessageRegex:   regexp.MustCompile(`Error: ImagePullBackOff`),
		PlatformReason: "ImagePullError",
	},
	{
		EventReason:    "FailedScheduling",
		MessageRegex:   regexp.MustCompile(`.*didn't find available persistent volumes to bind.*`),
		PlatformReason: "PVMountError",
	},
	{
		EventReason:    "BackOff",
		MessageRegex:   regexp.MustCompile(`.*didn't find available persistent volumes to bind.*`),
		PlatformReason: "StartContainerError",
	},
}

//reason: Killing
//involvedObject:
//	kind: Pod
//	name: aa-6d8cdc5c4b-z888d
//	namespace: app
//type: Normal

type eventLabel struct {
	EventID           string
	EventReason       string
	PlatformReason    string
	ResourceKind      string
	ResourceName      string
	ResourceNamespace string
	EventType         string
}

type eventCount struct {
	count          uint64
	lastChangeTime time.Time
}

func (ec *eventCount) updateCount(newCount uint64, lastTimestamp time.Time) {
	if newCount > ec.count && lastTimestamp.After(ec.lastChangeTime) {
		ec.count = newCount
		ec.lastChangeTime = lastTimestamp
	}
}

type prometheusSink struct {
	Cache    map[eventLabel]*eventCount
	Lock     sync.RWMutex
	StopSign chan struct{}
}

//eventLabel{
//EventReason:       event.Reason,
//PlatformReason:    getPlatformReason(event.Reason, event.Message),
//ResourceKind:      event.InvolvedObject.Kind,
//ResourceName:      event.InvolvedObject.Name,
//ResourceNamespace: event.InvolvedObject.Namespace,
//EventType:         event.Type,
//}

var (
	eventCountDesc = prometheus.NewDesc(
		"kube_event_count",
		"Number of kube event.",
		[]string{
			"event_id",
			"event_type",
			"resource_kind",
			"resource_namespace",
			"resource_name",
			"event_reason",
			"platform_reason"},
		nil,
	)
	cacheMaplengthDescGauge = prometheus.NewDesc(
		"kube_event_cache_number_gauge",
		"Number of kube event cache in memory",
		[]string{}, nil)
)

func (p *prometheusSink) Describe(descs chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(p, descs)
}

func (p *prometheusSink) Collect(metrics chan<- prometheus.Metric) {
	p.Lock.Lock()
	defer p.Lock.Unlock()

	for label, count := range p.Cache {
		metrics <- prometheus.MustNewConstMetric(
			eventCountDesc, prometheus.CounterValue, float64(count.count),
			label.EventID,
			label.EventType,
			label.ResourceKind,
			label.ResourceNamespace,
			label.ResourceName,
			label.EventReason,
			label.PlatformReason)
	}

	metrics <- prometheus.MustNewConstMetric(
		cacheMaplengthDescGauge, prometheus.GaugeValue, float64(len(p.Cache)))
}

func (p *prometheusSink) Name() string {
	return "Prometheus Sink"
}

func (p *prometheusSink) ExportEvents(batch *core.EventBatch) {

	p.Lock.Lock()
	defer p.Lock.Unlock()

	ttl := time.Now().Add(DefaultCacheTTL * -1)
	for _, event := range batch.Events {

		if event.LastTimestamp.Time.Before(ttl) {
			continue
		}

		l := eventLabel{
			EventID: event.Name,
			EventReason:       event.Reason,
			PlatformReason:    getPlatformReason(event.Reason, event.Message),
			ResourceKind:      event.InvolvedObject.Kind,
			ResourceName:      event.InvolvedObject.Name,
			ResourceNamespace: event.InvolvedObject.Namespace,
			EventType:         event.Type,
		}
		if e, ok := p.Cache[l]; ok {
			e.updateCount(uint64(event.Count), event.LastTimestamp.Time)
		} else {
			p.Cache[l] = &eventCount{
				count:          uint64(event.Count),
				lastChangeTime: event.LastTimestamp.Time,
			}
		}
	}

}
func (p *prometheusSink) Stop() {
	p.StopSign <- struct{}{}
}

func (p *prometheusSink) DeleteTimeoutCache() {
	p.Lock.Lock()
	defer p.Lock.Unlock()

	t := time.Now().Add(DefaultCacheTTL * -1)
	for label, count := range p.Cache {
		if count.lastChangeTime.Before(t) {
			delete(p.Cache, label)
		}
	}
}

func getPlatformReason(eventReason string, message string) string {
	for _, rule := range DefaultRules {
		if eventReason == rule.EventReason && rule.MessageRegex.MatchString(message) {
			return rule.PlatformReason
		}
	}
	return UnknownPlatformReason
}

func CreatePrometheusSink(uri *url.URL) (core.EventSink, error) {
	ps := &prometheusSink{
		Cache: map[eventLabel]*eventCount{},
	}
	go func() {
		t := time.NewTicker(DefaultDeleteCacheInterval)
		for {
			select {
			case <-ps.StopSign:
				return
			case <-t.C:
				ps.DeleteTimeoutCache()
			}
		}
	}()

	prometheus.MustRegister(ps)
	return ps, nil
}
