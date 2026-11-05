package syncer

import (
	"fmt"
	"net/netip"

	"github.com/yanet-platform/xnetip"
)

const (
	maxCIDRFragments   = 65536
	maxCIDRComparisons = 1000000
)

type ExclusionStats struct {
	InputEntries            int `json:"exclusion_input_entries"`
	IncludedEntriesAffected int `json:"included_entries_affected"`
	OutputEntries           int `json:"entries_after_exclusions"`
}

func SubtractIPv4Entries(includes, exclusions map[string]string) (map[string]string, ExclusionStats, error) {
	return subtractIPv4Entries(includes, exclusions, maxCIDRFragments, maxCIDRComparisons)
}

func subtractIPv4Entries(includes, exclusions map[string]string, fragmentLimit, comparisonLimit int) (map[string]string, ExclusionStats, error) {
	stats := ExclusionStats{InputEntries: len(exclusions)}
	exclusionBlocks := make([]xnetip.Contiguous[xnetip.Network4], 0, len(exclusions))
	for entry := range exclusions {
		block, err := parseContiguousIPv4(entry)
		if err != nil {
			return nil, stats, err
		}
		exclusionBlocks = append(exclusionBlocks, block)
	}
	exclusionBlocks = xnetip.AggregateContiguous(exclusionBlocks)

	output := make(map[string]string, len(includes))
	comparisons := 0
	for entry, source := range includes {
		includeBlock, err := parseContiguousIPv4(entry)
		if err != nil {
			return nil, stats, err
		}
		fragments := []xnetip.Contiguous[xnetip.Network4]{includeBlock}
		affected := false
		for _, exclusion := range exclusionBlocks {
			next := make([]xnetip.Contiguous[xnetip.Network4], 0, len(fragments))
			for _, fragment := range fragments {
				comparisons++
				if comparisons > comparisonLimit {
					return nil, stats, fmt.Errorf("IPv4 exclusions exceed the %d CIDR comparison limit", comparisonLimit)
				}
				unchanged := false
				for part := range fragment.Difference(exclusion) {
					if part.Prefix() == fragment.Prefix() {
						unchanged = true
					}
					next = append(next, part)
					if len(output)+len(next) > fragmentLimit {
						return nil, stats, fmt.Errorf("IPv4 exclusions exceed the %d CIDR fragment limit", fragmentLimit)
					}
				}
				if !unchanged {
					affected = true
				}
			}
			fragments = next
			if len(fragments) == 0 {
				break
			}
		}
		if affected {
			stats.IncludedEntriesAffected++
		}
		for _, fragment := range fragments {
			prefix := fragment.Prefix()
			value := prefix.String()
			if prefix.Bits() == 32 {
				value = prefix.Addr().String()
			}
			addResolvedSource(output, value, source)
		}
		if len(output) > fragmentLimit {
			return nil, stats, fmt.Errorf("IPv4 exclusions exceed the %d CIDR fragment limit", fragmentLimit)
		}
	}
	stats.OutputEntries = len(output)
	return output, stats, nil
}

func parseContiguousIPv4(value string) (xnetip.Contiguous[xnetip.Network4], error) {
	var prefix netip.Prefix
	if addr, err := netip.ParseAddr(value); err == nil && addr.Is4() {
		prefix = netip.PrefixFrom(addr, 32)
	} else {
		parsed, err := netip.ParsePrefix(value)
		if err != nil || !parsed.Addr().Is4() {
			return xnetip.Contiguous[xnetip.Network4]{}, fmt.Errorf("invalid IPv4 exclusion or include %q", value)
		}
		prefix = parsed
	}
	block, ok := xnetip.ContiguousFromPrefix4(prefix)
	if !ok {
		return xnetip.Contiguous[xnetip.Network4]{}, fmt.Errorf("invalid IPv4 prefix %q", value)
	}
	return block, nil
}
