package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/struCoder/pidusage"
)

func NewStdoutMetricTracker(ctx context.Context, interval time.Duration) MetricTracker {
	return CollectMetrics(ctx, interval, func(m Metric) {
		log.Printf(
			"[metric] %s | %d stream(s) | %d%% (CPU) | %d byte(s) (HEAP)\n",
			time.UnixMilli(m.Timestamp), m.ActiveStreams, m.CpuPercentage, m.MemoryHeapBytes,
		)
	})
}

func NewCSVMetricTracker(ctx context.Context, interval time.Duration, filepath string) MetricTracker {
	file, err := os.Create(filepath)
	if err != nil {
		log.Fatalf("create CSV Metrics file: %v", err)
		return nil
	}

	writer := csv.NewWriter(file)

	return CollectMetrics(ctx, interval, func(m Metric) {
		writer.Write([]string{
			strconv.FormatInt(m.Timestamp, 10),
			strconv.FormatUint(uint64(m.ActiveStreams), 10),
			strconv.FormatUint(uint64(m.CpuPercentage), 10),
			strconv.FormatUint(uint64(m.MemoryHeapBytes), 10),
			strconv.FormatUint(uint64(m.BytesRead), 10),
			strconv.FormatUint(uint64(m.BytesWritten), 10),
		})
		writer.Flush()
	})
}

func ReadCsvMetrics(filepath string) ([]Metric, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("open csv metrics file %q: %w", filepath, err)
	}
	defer f.Close()

	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv metrics file %q: %w", filepath, err)
	}

	metrics := make([]Metric, 0, len(records))
	for n, record := range records {
		metric, err := parseCsvMetric(record)
		if err != nil {
			return nil, fmt.Errorf("parse metric from csv file %q record #%d: %w", filepath, n, err)
		}
		metrics = append(metrics, metric)
	}
	return metrics, nil
}

type (
	metricValue interface {
		uint | uint64
	}

	metricAggregator[T metricValue] struct {
		n int

		min, max, avg T
	}

	aggregatedMetrics[T metricValue] struct {
		Min T
		Max T
		Avg T
	}
)

func (a *metricAggregator[T]) Add(v T) {
	if a.n += 1; a.n == 1 {
		a.min = v
		a.max = v
		a.avg = v
		return
	}

	a.avg += v
	if v < a.min {
		a.min = v
	}
	if v > a.max {
		a.max = v
	}
}

func (a *metricAggregator[T]) Metrics() aggregatedMetrics[T] {
	var avg T
	if a.n > 0 {
		avg = a.avg / T(a.n)
	}
	return aggregatedMetrics[T]{
		Min: a.min,
		Max: a.max,
		Avg: avg,
	}
}

func PrintMetricStats(metrics []Metric, activeStreams uint32) {
	var (
		cpuPercentageAgg   metricAggregator[uint]
		memoryHeapBytesAgg metricAggregator[uint64]
		bytesReadAgg       metricAggregator[uint64]
		bytesWrittenAgg    metricAggregator[uint64]
	)

	for _, metric := range metrics {
		if metric.ActiveStreams != activeStreams {
			continue
		}

		cpuPercentageAgg.Add(metric.CpuPercentage)
		memoryHeapBytesAgg.Add(metric.MemoryHeapBytes)
		bytesReadAgg.Add(metric.BytesRead)
		bytesWrittenAgg.Add(metric.BytesWritten)
	}

	cpuPercentageMetrics := cpuPercentageAgg.Metrics()
	memoryHeapBytesMetrics := memoryHeapBytesAgg.Metrics()
	bytesReadMetrics := bytesReadAgg.Metrics()
	bytesWrittenMetrics := bytesWrittenAgg.Metrics()

	// print above metrics to stdout
	fmt.Printf(`Active Streams: %d
	
CPU (%%):
	- Min: %d
	- Max: %d
	- Avg: %d

Memory Heap (MiB):
	- Min: %.3f
	- Max: %.3f
	- Avg: %.3f

Bytes Read (KiB):
	- Min: %.3f
	- Max: %.3f
	- Avg: %.3f

Bytes Written (KiB):
	- Min: %.3f
	- Max: %.3f
	- Avg: %.3f
`,
		activeStreams,
		cpuPercentageMetrics.Min,
		cpuPercentageMetrics.Max,
		cpuPercentageMetrics.Avg,
		mib(memoryHeapBytesMetrics.Min),
		mib(memoryHeapBytesMetrics.Max),
		mib(memoryHeapBytesMetrics.Avg),
		kib(bytesReadMetrics.Min),
		kib(bytesReadMetrics.Max),
		kib(bytesReadMetrics.Avg),
		kib(bytesWrittenMetrics.Min),
		kib(bytesWrittenMetrics.Max),
		kib(bytesWrittenMetrics.Avg),
	)
}

func kib(bytes uint64) float64 {
	return float64(bytes) / 1024
}

func mib(bytes uint64) float64 {
	return float64(bytes) / 1024 / 1024
}

func parseCsvMetric(record []string) (Metric, error) {
	timestamp, err := strconv.ParseInt(record[0], 10, 64)
	if err != nil {
		return Metric{}, fmt.Errorf("parse timestamp: %w", err)
	}
	activeStreams, err := strconv.ParseUint(record[1], 10, 64)
	if err != nil {
		return Metric{}, fmt.Errorf("parse active streams: %w", err)
	}
	cpuPercentage, err := strconv.ParseUint(record[2], 10, 64)
	if err != nil {
		return Metric{}, fmt.Errorf("parse cpu percentage: %w", err)
	}
	memoryHeapBytes, err := strconv.ParseUint(record[3], 10, 64)
	if err != nil {
		return Metric{}, fmt.Errorf("parse memory heap bytes: %w", err)
	}
	bytesRead, err := strconv.ParseUint(record[4], 10, 64)
	if err != nil {
		return Metric{}, fmt.Errorf("parse memory heap bytes: %w", err)
	}
	bytesWritten, err := strconv.ParseUint(record[5], 10, 64)
	if err != nil {
		return Metric{}, fmt.Errorf("parse memory heap bytes: %w", err)
	}
	return Metric{
		Timestamp:       timestamp,
		ActiveStreams:   uint32(activeStreams),
		CpuPercentage:   uint(cpuPercentage),
		MemoryHeapBytes: memoryHeapBytes,
		BytesRead:       bytesRead,
		BytesWritten:    bytesWritten,
	}, nil
}

func NewNoopMetricTracker(context.Context, time.Duration) MetricTracker {
	return DummyMetricTracker{}
}

func CollectMetrics(ctx context.Context, interval time.Duration, cb func(Metric)) MetricTracker {
	var collector MetricCollector
	collector.Start(ctx, interval, cb)
	return &collector
}

type (
	// Collects metrics each interval and writes them to a csv file.
	//
	// - Incoming streams are collected manually
	// - CPU / Memory is collected using https://github.com/shirou/gopsutil
	MetricCollector struct {
		started       bool
		activeStreams uint32
		bytesRead     uint64
		bytesWritten  uint64
	}

	// Metric is a single metric collected by the MetricCollector.
	Metric struct {
		Timestamp       int64
		ActiveStreams   uint32
		BytesRead       uint64
		BytesWritten    uint64
		CpuPercentage   uint
		MemoryHeapBytes uint64
	}

	MetricTracker interface {
		AddIncomingStream() uint32
		SubIncomingStream() uint32

		AddBytesRead(uint64) uint64
		AddBytesWritten(uint64) uint64
	}
)

func (c *MetricCollector) AddIncomingStream() uint32 {
	return atomic.AddUint32(&c.activeStreams, 1)
}

func (c *MetricCollector) SubIncomingStream() uint32 {
	return atomic.AddUint32(&c.activeStreams, ^uint32(0))
}

func (c *MetricCollector) AddBytesRead(n uint64) uint64 {
	return atomic.AddUint64(&c.bytesRead, n)
}

func (c *MetricCollector) AddBytesWritten(n uint64) uint64 {
	return atomic.AddUint64(&c.bytesWritten, n)
}

func (c *MetricCollector) Start(ctx context.Context, interval time.Duration, cb func(Metric)) {
	if c.started {
		panic("MetricCollector already started")
	}
	c.started = true
	pid := os.Getpid()
	cpu := runtime.NumCPU()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
				cb(c.collect(interval, pid, cpu))
			}
		}
	}()
}

func (c *MetricCollector) collect(interval time.Duration, pid, cpu int) Metric {
	// metric timestamp in ms
	ts := time.Now().UnixMilli()

	// track current incoming streams
	activeStreams := atomic.LoadUint32(&c.activeStreams)

	// track CPU usage
	sysInfo, err := pidusage.GetStat(pid)
	if err != nil {
		sysInfo = new(pidusage.SysInfo)
	}
	cpuPercentage := uint(sysInfo.CPU)

	// track Memory usage (percentage + bytes)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memUsage := m.HeapAlloc

	// track bytes read / written
	bytesRead := atomic.SwapUint64(&c.bytesRead, 0)
	bytesWritten := atomic.SwapUint64(&c.bytesWritten, 0)

	// return all metrics
	return Metric{
		Timestamp:       ts,
		ActiveStreams:   activeStreams,
		CpuPercentage:   cpuPercentage,
		MemoryHeapBytes: memUsage,
		BytesRead:       bytesRead,
		BytesWritten:    bytesWritten,
	}
}

type DummyMetricTracker struct{}

func (DummyMetricTracker) AddIncomingStream() uint32     { return 0 }
func (DummyMetricTracker) SubIncomingStream() uint32     { return 0 }
func (DummyMetricTracker) AddBytesRead(uint64) uint64    { return 0 }
func (DummyMetricTracker) AddBytesWritten(uint64) uint64 { return 0 }
