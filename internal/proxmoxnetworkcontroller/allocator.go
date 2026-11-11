/*
Copyright 2025 The Cozystack Authors.

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

package proxmoxnetworkcontroller

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	minVLAN = 1
	maxVLAN = 4094
)

// errZoneExhausted is returned when every VLAN of a zone is taken.
var errZoneExhausted = errors.New("no free VLAN left in the zone")

// vlanSet is a set of 802.1Q IDs.
type vlanSet map[int32]struct{}

func (s vlanSet) has(v int32) bool { _, ok := s[v]; return ok }
func (s vlanSet) add(v int32)      { s[v] = struct{}{} }

// parseVLANRanges turns "101-199" and "120" entries into a sorted, de-duplicated
// list of IDs. It rejects IDs outside 1..4094 and ranges written backwards, so
// a typo in a zone fails loudly instead of yielding an empty or huge range.
func parseVLANRanges(ranges []string) ([]int32, error) {
	set := vlanSet{}
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		lo, hi, isRange := strings.Cut(r, "-")
		from, err := parseVLAN(lo)
		if err != nil {
			return nil, fmt.Errorf("vlan range %q: %w", r, err)
		}
		to := from
		if isRange {
			if to, err = parseVLAN(hi); err != nil {
				return nil, fmt.Errorf("vlan range %q: %w", r, err)
			}
		}
		if to < from {
			return nil, fmt.Errorf("vlan range %q ends before it starts", r)
		}
		for v := from; v <= to; v++ {
			set.add(v)
		}
	}
	out := make([]int32, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func parseVLAN(s string) (int32, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if n < minVLAN || n > maxVLAN {
		return 0, fmt.Errorf("%d is outside %d-%d", n, minVLAN, maxVLAN)
	}
	return int32(n), nil
}

// allocatable is the zone's candidate list minus its reserved IDs.
func allocatable(ranges []string, reserved []int32) ([]int32, error) {
	ids, err := parseVLANRanges(ranges)
	if err != nil {
		return nil, err
	}
	res := vlanSet{}
	for _, r := range reserved {
		res.add(r)
	}
	out := ids[:0]
	for _, id := range ids {
		if !res.has(id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// firstFree returns the lowest candidate not in used. Lowest-first keeps the
// allocation deterministic for a given set of live Vlans, which is what makes a
// re-run after a crash pick the same ID.
func firstFree(candidates []int32, used vlanSet) (int32, error) {
	for _, id := range candidates {
		if !used.has(id) {
			return id, nil
		}
	}
	return 0, errZoneExhausted
}

// countFree counts candidates not in used.
func countFree(candidates []int32, used vlanSet) int32 {
	var n int32
	for _, id := range candidates {
		if !used.has(id) {
			n++
		}
	}
	return n
}
