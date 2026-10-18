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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestReference(t *testing.T) {
	docs, err := Reference()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range docs {
		if d.Name == "" || d.Help == "" || d.Type == "" {
			t.Errorf("incomplete: %+v", d)
		}
		if seen[d.Name] {
			t.Errorf("listed twice: %s", d.Name)
		}
		seen[d.Name] = true
	}
	ch := make(chan *prometheus.Desc, 64)
	(&Collector{}).Describe(ch)
	close(ch)
	if want := len(registered) + len(ch); len(docs) != want {
		t.Errorf("%d metrics, want %d", len(docs), want)
	}
	want := map[string][]string{
		"odoo_operator_jobs_total":       {"namespace", "name", "kind", "result"},
		"odoo_operator_deployment_phase": {"namespace", "name", "phase"},
		"odoo_operator_build_info":       {"version"},
	}
	for _, d := range docs {
		if labels, ok := want[d.Name]; ok {
			if len(labels) != len(d.Labels) {
				t.Errorf("%s labels = %v, want %v", d.Name, d.Labels, labels)
			}
			delete(want, d.Name)
		}
	}
	for name := range want {
		t.Errorf("%s is missing from the reference", name)
	}
}
