package syncer

import (
	"fmt"
	"net/netip"
	"sort"
)

const minCollapsedIPv4Prefix = 24

// OptimizationStats summarizes the effects of deduplication and CIDR collapse.
type OptimizationStats struct {
	InputEntries          int `json:"input_entries"`
	UniqueEntries         int `json:"unique_entries"`
	DuplicateEntries      int `json:"duplicate_entries"`
	CoveredEntriesRemoved int `json:"covered_entries_removed"`
	CIDRBlocks            int `json:"cidr_blocks"`
	OutputEntries         int `json:"output_entries"`
	EntriesSaved          int `json:"entries_saved"`
}

type ipv4Range struct {
	start uint64
	end   uint64
	value string
}

// OptimizeIPv4Entries removes duplicates and entries fully covered by another
// entry. When collapseCIDRs is true, it also emits the smallest exact CIDR cover
// whose generated prefixes are /24 or larger; remaining addresses stay as IPs.
func OptimizeIPv4Entries(entries []string, collapseCIDRs bool) ([]string, OptimizationStats, error) {
	stats := OptimizationStats{InputEntries: len(entries)}
	unique := make(map[string]ipv4Range, len(entries))
	for _, entry := range entries {
		rng, err := parseIPv4Range(entry)
		if err != nil {
			return nil, stats, err
		}
		if _, exists := unique[rng.value]; exists {
			continue
		}
		unique[rng.value] = rng
	}
	stats.UniqueEntries = len(unique)
	stats.DuplicateEntries = stats.InputEntries - stats.UniqueEntries

	ranges := make([]ipv4Range, 0, len(unique))
	for _, rng := range unique {
		ranges = append(ranges, rng)
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		if ranges[i].end != ranges[j].end {
			return ranges[i].end > ranges[j].end
		}
		return ranges[i].value < ranges[j].value
	})

	// Remove a range only when the already-seen union fully covers it.
	covered := make([]ipv4Range, 0, len(ranges))
	var coveredEnd uint64
	hasCoverage := false
	for _, rng := range ranges {
		if hasCoverage && rng.end <= coveredEnd {
			stats.CoveredEntriesRemoved++
			continue
		}
		covered = append(covered, rng)
		if !hasCoverage || rng.end > coveredEnd {
			coveredEnd = rng.end
			hasCoverage = true
		}
	}

	var output []string
	if collapseCIDRs {
		output = collapseIPv4Ranges(covered)
	} else {
		output = make([]string, 0, len(covered))
		for _, rng := range covered {
			output = append(output, rng.value)
		}
	}
	for _, entry := range output {
		if _, err := netip.ParsePrefix(entry); err == nil {
			stats.CIDRBlocks++
		}
	}
	stats.OutputEntries = len(output)
	stats.EntriesSaved = stats.InputEntries - stats.OutputEntries
	return output, stats, nil
}

func parseIPv4Range(value string) (ipv4Range, error) {
	if addr, err := netip.ParseAddr(value); err == nil && addr.Is4() {
		n := ipv4Uint(addr)
		return ipv4Range{start: n, end: n, value: addr.String()}, nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() {
		return ipv4Range{}, fmt.Errorf("invalid IPv4 address or CIDR %q", value)
	}
	prefix = prefix.Masked()
	start := ipv4Uint(prefix.Addr())
	span := uint64(1) << uint(32-prefix.Bits())
	canonical := prefix.String()
	if prefix.Bits() == 32 {
		canonical = prefix.Addr().String()
	}
	return ipv4Range{start: start, end: start + span - 1, value: canonical}, nil
}

func ipv4Uint(addr netip.Addr) uint64 {
	b := addr.As4()
	return uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
}

func uintIPv4(value uint64) netip.Addr {
	return netip.AddrFrom4([4]byte{
		byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value),
	})
}

func collapseIPv4Ranges(ranges []ipv4Range) []string {
	preservedSmallCIDRs := make(map[uint64][]ipv4Range)
	for _, rng := range ranges {
		prefix, err := netip.ParsePrefix(rng.value)
		if err == nil && prefix.Bits() > minCollapsedIPv4Prefix {
			preservedSmallCIDRs[rng.start] = append(preservedSmallCIDRs[rng.start], rng)
		}
	}
	for start := range preservedSmallCIDRs {
		sort.Slice(preservedSmallCIDRs[start], func(i, j int) bool {
			return preservedSmallCIDRs[start][i].end > preservedSmallCIDRs[start][j].end
		})
	}

	merged := make([]ipv4Range, 0, len(ranges))
	for _, rng := range ranges {
		last := len(merged) - 1
		if last < 0 || rng.start > merged[last].end+1 {
			merged = append(merged, ipv4Range{start: rng.start, end: rng.end})
			continue
		}
		if rng.end > merged[last].end {
			merged[last].end = rng.end
		}
	}

	var output []string
	for _, rng := range merged {
		for current := rng.start; current <= rng.end; {
			prefix := 32
			for candidate := 0; candidate <= minCollapsedIPv4Prefix; candidate++ {
				blockSize := uint64(1) << uint(32-candidate)
				if current%blockSize == 0 && blockSize <= rng.end-current+1 {
					prefix = candidate
					break
				}
			}
			if prefix == 32 {
				for _, existing := range preservedSmallCIDRs[current] {
					if existing.end <= rng.end {
						output = append(output, existing.value)
						current = existing.end + 1
						prefix = 0
						break
					}
				}
				if prefix == 0 {
					continue
				}
			}
			blockSize := uint64(1) << uint(32-prefix)
			if prefix == 32 {
				output = append(output, uintIPv4(current).String())
			} else {
				output = append(output, netip.PrefixFrom(uintIPv4(current), prefix).String())
			}
			current += blockSize
		}
	}
	return output
}
