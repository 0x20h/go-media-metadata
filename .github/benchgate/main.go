// Command benchgate compares two sets of `go test -bench -benchmem`
// results, base and head, and exits 1 on a clear regression:
//
//   - allocs/op or read-B/op higher at all (both are deterministic);
//   - B/op more than 10% higher;
//   - ns/op more than 25% higher at the median, with head's fastest run
//     still slower than base's median (shared runners are noisy).
//
// Benchmarks only in one of the two files are listed and not compared.
//
// Usage: go run ./.github/benchgate base.txt head.txt
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// samples maps benchmark name, then unit, to its values over the runs.
type samples map[string]map[string][]float64

var procSuffix = regexp.MustCompile(`-\d+$`)

func parse(path string) (samples, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := samples{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		name := procSuffix.ReplaceAllString(fields[0], "")
		if out[name] == nil {
			out[name] = map[string][]float64{}
		}
		// fields[1] is the iteration count; then value, unit pairs.
		for i := 2; i+1 < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q: %v", path, sc.Text(), err)
			}
			out[name][fields[i+1]] = append(out[name][fields[i+1]], v)
		}
	}
	return out, sc.Err()
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func minOf(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		m = min(m, x)
	}
	return m
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: benchgate base.txt head.txt")
		os.Exit(2)
	}
	base, err := parse(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	head, err := parse(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var names []string
	for n := range head {
		names = append(names, n)
	}
	sort.Strings(names)
	failed := false
	fmt.Printf("%-60s %-12s %14s %14s %8s\n", "benchmark", "unit", "base", "head", "delta")
	for _, n := range names {
		b, ok := base[n]
		if !ok {
			fmt.Printf("%-60s new, not compared\n", n)
			continue
		}
		for _, unit := range []string{"ns/op", "B/op", "allocs/op", "read-B/op"} {
			bv, hv := b[unit], head[n][unit]
			if len(bv) == 0 || len(hv) == 0 {
				continue
			}
			bm, hm := median(bv), median(hv)
			delta := 0.0
			if bm > 0 {
				delta = (hm - bm) / bm * 100
			}
			var bad bool
			switch unit {
			case "allocs/op", "read-B/op":
				bad = hm > bm
			case "B/op":
				bad = hm > bm*1.10
			case "ns/op":
				bad = hm > bm*1.25 && minOf(hv) > bm
			}
			mark := ""
			if bad {
				mark, failed = "  REGRESSION", true
			}
			fmt.Printf("%-60s %-12s %14.1f %14.1f %+7.1f%%%s\n", n, unit, bm, hm, delta, mark)
		}
	}
	for n := range base {
		if _, ok := head[n]; !ok {
			fmt.Printf("%-60s removed\n", n)
		}
	}
	if failed {
		os.Exit(1)
	}
}
