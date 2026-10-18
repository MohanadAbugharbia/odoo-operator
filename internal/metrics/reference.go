/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricDoc describes one metric the operator exports, for the generated
// metrics reference (hack/docs).
type MetricDoc struct {
	Name   string
	Type   string // counter, gauge or histogram
	Help   string
	Labels []string
	// ScrapeTime is true for the series read from OdooDeployment status at
	// scrape time, which only the elected leader reports.
	ScrapeTime bool
}

// descRE takes a Desc apart; prometheus.Desc has no accessors, only String.
var descRE = regexp.MustCompile(`^Desc\{fqName: ("(?:[^"\\]|\\.)*"), help: ("(?:[^"\\]|\\.)*"), constLabels: \{[^}]*\}, variableLabels: \{([^}]*)\}\}$`)

// Reference lists every metric the operator registers, sorted by name.
func Reference() ([]MetricDoc, error) {
	var out []MetricDoc
	add := func(c prometheus.Collector, typ string, scrapeTime bool) error {
		ch := make(chan *prometheus.Desc, 32)
		go func() { c.Describe(ch); close(ch) }()
		for d := range ch {
			doc, err := parseDesc(d)
			if err != nil {
				return err
			}
			doc.Type, doc.ScrapeTime = typ, scrapeTime
			out = append(out, doc)
		}
		return nil
	}
	for _, m := range registered {
		if err := add(m.collector, m.typ, false); err != nil {
			return nil, err
		}
	}
	// collectOne reports every scrape-time series as a gauge.
	if err := add(&Collector{}, typeGauge, true); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b MetricDoc) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func parseDesc(d *prometheus.Desc) (MetricDoc, error) {
	m := descRE.FindStringSubmatch(d.String())
	if m == nil {
		return MetricDoc{}, fmt.Errorf("cannot read %s", d)
	}
	name, err := strconv.Unquote(m[1])
	if err != nil {
		return MetricDoc{}, err
	}
	help, err := strconv.Unquote(m[2])
	if err != nil {
		return MetricDoc{}, err
	}
	var labels []string
	if m[3] != "" {
		for _, l := range strings.Split(m[3], ",") {
			labels = append(labels, strings.TrimSuffix(strings.TrimPrefix(l, "c("), ")"))
		}
	}
	return MetricDoc{Name: name, Help: help, Labels: labels}, nil
}
