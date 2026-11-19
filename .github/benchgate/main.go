// Command benchgate compares two sets of `go test -bench -benchmem`
// results, base and head, and exits 1 on a clear regression:
//
//   - allocs/op, read-B/op or readcalls/op higher at all (all three are
//     deterministic), beyond what allow.txt allows (see allowances);
//   - B/op more than 10% higher;
//   - ns/op more than 25% higher at the median, with head's fastest run
//     still slower than base's median (shared runners are noisy).
//
// Benchmarks only in one of the two files are listed and not compared.
//
// Usage: go run ./.github/benchgate base.txt head.txt
//
// allow.txt, next to this file, lists rises a change may make on purpose,
// one per line: a benchmark name (a prefix of it, as the report prints
// it), a unit, the most it may rise by, and the reason. For allocs/op,
// read-B/op and readcalls/op the most is in that unit; for B/op and ns/op
// it's a percentage ("160%") that takes the place of the 10% or 25% where
// it's higher. Lines starting with # are comments. A PR that adds one
// says why; the file is emptied again once the change is on main and is
// the new base.
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
	allow, err := readAllowances(allowFile())
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
		for _, unit := range []string{"ns/op", "B/op", "allocs/op", "read-B/op", "readcalls/op"} {
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
			case "allocs/op", "read-B/op", "readcalls/op":
				bad = hm > bm+allowed(allow, n, unit)
			case "B/op":
				bad = hm > bm*(1+max(10, allowed(allow, n, unit))/100)
			case "ns/op":
				// The limit, and head's fastest run beyond what the noise
				// margin of 25% leaves: base's median, at the default.
				limit := bm * (1 + max(25, allowed(allow, n, unit))/100)
				bad = hm > limit && minOf(hv) > limit/1.25
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

// allowance is a rise allow.txt allows: of unit, in the benchmarks whose
// name starts with prefix.
type allowance struct {
	prefix, unit string
	max          float64
}

// allowFile is allow.txt next to this program's source.
func allowFile() string {
	if p := os.Getenv("BENCHGATE_ALLOW"); p != "" {
		return p
	}
	return ".github/benchgate/allow.txt"
}

// readAllowances reads allow.txt; a missing file allows nothing.
func readAllowances(path string) ([]allowance, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []allowance
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			return nil, fmt.Errorf("%s:%d: want a benchmark, a unit, a most and a reason", path, i+1)
		}
		num, pct := strings.CutSuffix(f[2], "%")
		max, err := strconv.ParseFloat(num, 64)
		if err != nil || max < 0 {
			return nil, fmt.Errorf("%s:%d: %q isn't a rise", path, i+1, f[2])
		}
		switch f[1] {
		case "allocs/op", "read-B/op", "readcalls/op":
			if pct {
				return nil, fmt.Errorf("%s:%d: %s rises by a count, not a percentage", path, i+1, f[1])
			}
		case "B/op", "ns/op":
			if !pct {
				return nil, fmt.Errorf("%s:%d: %s rises by a percentage (\"160%%\")", path, i+1, f[1])
			}
		default:
			return nil, fmt.Errorf("%s:%d: unit %q can't be allowed a rise", path, i+1, f[1])
		}
		out = append(out, allowance{f[0], f[1], max})
	}
	return out, nil
}

// allowed is the rise allowed for unit in benchmark name.
func allowed(allow []allowance, name, unit string) float64 {
	m := 0.0
	for _, a := range allow {
		if a.unit == unit && strings.HasPrefix(name, a.prefix) {
			m = max(m, a.max)
		}
	}
	return m
}
