package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu         sync.Mutex
	gauges     map[string]*gauge
	counters   map[string]*counter
	histograms map[string]*histogram
}

type Gauge struct {
	reg  *Registry
	name string
}

type Counter struct {
	reg  *Registry
	name string
}

type Histogram struct {
	reg  *Registry
	name string
}

type Label struct {
	Name  string
	Value string
}

type gauge struct {
	help   string
	values map[string]sample
}

type counter struct {
	help   string
	values map[string]sample
}

type histogram struct {
	help      string
	boundary  []float64
	instances map[string]*histogramInstance
}

type histogramInstance struct {
	labels  []Label
	buckets []uint64
	count   uint64
	sum     float64
}

type sample struct {
	labels []Label
	value  float64
}

func NewRegistry() *Registry {
	return &Registry{
		gauges:     make(map[string]*gauge),
		counters:   make(map[string]*counter),
		histograms: make(map[string]*histogram),
	}
}

func (r *Registry) NewGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.gauges[name]; !ok {
		r.gauges[name] = &gauge{help: help, values: make(map[string]sample)}
	}
	return &Gauge{reg: r, name: name}
}

func (r *Registry) NewCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.counters[name]; !ok {
		r.counters[name] = &counter{help: help, values: make(map[string]sample)}
	}
	return &Counter{reg: r, name: name}
}

func (r *Registry) NewHistogram(name, help string, boundaries []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.histograms[name]; !ok {
		sorted := append([]float64(nil), boundaries...)
		sort.Float64s(sorted)
		r.histograms[name] = &histogram{help: help, boundary: sorted, instances: make(map[string]*histogramInstance)}
	}
	return &Histogram{reg: r, name: name}
}

func (g *Gauge) Set(value float64, labels ...Label) {
	g.reg.mu.Lock()
	defer g.reg.mu.Unlock()
	key, sorted := labelKey(labels)
	g.reg.gauges[g.name].values[key] = sample{labels: sorted, value: value}
}

func (c *Counter) Inc(labels ...Label) {
	c.Add(1, labels...)
}

func (c *Counter) Add(value float64, labels ...Label) {
	c.reg.mu.Lock()
	defer c.reg.mu.Unlock()
	key, sorted := labelKey(labels)
	s := c.reg.counters[c.name].values[key]
	s.labels = sorted
	s.value += value
	c.reg.counters[c.name].values[key] = s
}

func (h *Histogram) Observe(value float64, labels ...Label) {
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()
	key, sorted := labelKey(labels)
	hist := h.reg.histograms[h.name]
	inst := hist.instances[key]
	if inst == nil {
		inst = &histogramInstance{
			labels:  sorted,
			buckets: make([]uint64, len(hist.boundary)),
		}
		hist.instances[key] = inst
	}
	for i, upper := range hist.boundary {
		if value <= upper {
			inst.buckets[i]++
		}
	}
	inst.count++
	inst.sum += value
}

func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cw := &countingWriter{w: w}

	names := sortedKeys(r.gauges)
	for _, name := range names {
		g := r.gauges[name]
		if err := writeHeader(cw, name, g.help, "gauge"); err != nil {
			return cw.n, err
		}
		if err := writeSamples(cw, name, g.values); err != nil {
			return cw.n, err
		}
	}

	names = sortedKeys(r.counters)
	for _, name := range names {
		c := r.counters[name]
		if err := writeHeader(cw, name, c.help, "counter"); err != nil {
			return cw.n, err
		}
		if err := writeSamples(cw, name, c.values); err != nil {
			return cw.n, err
		}
	}

	names = sortedKeys(r.histograms)
	for _, name := range names {
		h := r.histograms[name]
		if err := writeHeader(cw, name, h.help, "histogram"); err != nil {
			return cw.n, err
		}
		keys := sortedSampleKeys(h.instances)
		for _, key := range keys {
			inst := h.instances[key]
			for i, upper := range h.boundary {
				labels := appendLabels(inst.labels, Label{Name: "le", Value: formatFloat(upper)})
				if _, err := fmt.Fprintf(cw, "%s_bucket%s %d\n", name, formatLabels(labels), inst.buckets[i]); err != nil {
					return cw.n, err
				}
			}
			labels := appendLabels(inst.labels, Label{Name: "le", Value: "+Inf"})
			if _, err := fmt.Fprintf(cw, "%s_bucket%s %d\n", name, formatLabels(labels), inst.count); err != nil {
				return cw.n, err
			}
			if _, err := fmt.Fprintf(cw, "%s_sum%s %s\n", name, formatLabels(inst.labels), formatFloat(inst.sum)); err != nil {
				return cw.n, err
			}
			if _, err := fmt.Fprintf(cw, "%s_count%s %d\n", name, formatLabels(inst.labels), inst.count); err != nil {
				return cw.n, err
			}
		}
	}

	return cw.n, nil
}

func writeHeader(w io.Writer, name, help, typ string) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n", name, help); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "# TYPE %s %s\n", name, typ)
	return err
}

func writeSamples(w io.Writer, name string, values map[string]sample) error {
	keys := sortedSampleKeys(values)
	for _, key := range keys {
		s := values[key]
		if _, err := fmt.Fprintf(w, "%s%s %s\n", name, formatLabels(s.labels), formatFloat(s.value)); err != nil {
			return err
		}
	}
	return nil
}

func labelKey(labels []Label) (string, []Label) {
	sorted := append([]Label(nil), labels...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	var b strings.Builder
	for _, label := range sorted {
		b.WriteString(label.Name)
		b.WriteByte('=')
		b.WriteString(label.Value)
		b.WriteByte('\xff')
	}
	return b.String(), sorted
}

func formatLabels(labels []Label) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, label.Name, escape(label.Value)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func appendLabels(labels []Label, extra Label) []Label {
	out := make([]Label, 0, len(labels)+1)
	out = append(out, labels...)
	out = append(out, extra)
	return out
}

func escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func formatFloat(value float64) string {
	return fmt.Sprintf("%g", value)
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSampleKeys[T any](m map[string]T) []string {
	return sortedKeys(m)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}
