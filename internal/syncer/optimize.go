package syncer

import (
	"fmt"
	"net/netip"
	"sort"

	"github.com/yanet-platform/xnetip"
)

// OptimizationStats summarizes the effects of deduplication and CIDR collapse.
type OptimizationStats struct {
	InputEntries            int `json:"input_entries"`
	UniqueEntries           int `json:"unique_entries"`
	DuplicateEntries        int `json:"duplicate_entries"`
	CoveredEntriesRemoved   int `json:"covered_entries_removed"`
	CIDRBlocks              int `json:"cidr_blocks"`
	CIDRsCreated            int `json:"cidrs_created"`
	ExclusionInputEntries   int `json:"exclusion_input_entries"`
	IncludedEntriesAffected int `json:"included_entries_affected"`
	EntriesAfterExclusions  int `json:"entries_after_exclusions"`
	OutputEntries           int `json:"output_entries"`
	EntriesSaved            int `json:"entries_saved"`
}

type ipv4Range struct {
	start uint64
	end   uint64
	value string
}

// OptimizeIPv4Entries removes duplicates and entries fully covered by another
// entry. When collapseCIDRs is true, it also emits the smallest exact CIDR cover;
// host routes stay as IPs.
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
		collapsed, err := collapseIPv4Ranges(covered)
		if err != nil {
			return nil, stats, err
		}
		output = collapsed
	} else {
		output = make([]string, 0, len(covered))
		for _, rng := range covered {
			output = append(output, rng.value)
		}
	}
	for _, entry := range output {
		if _, err := netip.ParsePrefix(entry); err == nil {
			stats.CIDRBlocks++
			if _, existed := unique[entry]; !existed {
				stats.CIDRsCreated++
			}
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

func collapseIPv4Ranges(ranges []ipv4Range) ([]string, error) {
	blocks := make([]xnetip.Contiguous[xnetip.Network4], 0, len(ranges))
	for _, rng := range ranges {
		prefix, err := netip.ParsePrefix(rng.value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(rng.value)
			if addrErr != nil || !addr.Is4() {
				return nil, fmt.Errorf("parse IPv4 entry %q for CIDR collapse", rng.value)
			}
			prefix = netip.PrefixFrom(addr, 32)
		}
		block, ok := xnetip.ContiguousFromPrefix4(prefix)
		if !ok {
			return nil, fmt.Errorf("convert IPv4 prefix %q for CIDR collapse", prefix)
		}
		blocks = append(blocks, block)
	}
	blocks = xnetip.AggregateContiguous(blocks)
	sort.Slice(blocks, func(i, j int) bool {
		return ipv4Uint(blocks[i].Prefix().Addr()) < ipv4Uint(blocks[j].Prefix().Addr())
	})

	output := make([]string, len(blocks))
	for i, block := range blocks {
		prefix := block.Prefix()
		if prefix.Bits() == 32 {
			output[i] = prefix.Addr().String()
		} else {
			output[i] = prefix.String()
		}
	}
	return output, nil
}
