package container

import (
	"iter"
	"sort"
)

// sortedPairs iterates a string map in key order, so generated argv is
// deterministic (important for golden tests and reproducible runs).
func sortedPairs(m map[string]string) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}
