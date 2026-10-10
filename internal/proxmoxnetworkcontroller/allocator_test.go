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
	"reflect"
	"testing"
)

func TestParseVLANRanges(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []int32
		wantErr bool
	}{
		{name: "single", in: []string{"120"}, want: []int32{120}},
		{name: "range", in: []string{"101-104"}, want: []int32{101, 102, 103, 104}},
		{name: "overlapping ranges are merged", in: []string{"101-103", "102-104", "103"}, want: []int32{101, 102, 103, 104}},
		{name: "spaces are tolerated", in: []string{" 7 - 8 "}, want: []int32{7, 8}},
		{name: "backwards range", in: []string{"200-100"}, wantErr: true},
		{name: "zero is not a VLAN", in: []string{"0-3"}, wantErr: true},
		{name: "4095 is reserved by 802.1Q", in: []string{"4090-4095"}, wantErr: true},
		{name: "not a number", in: []string{"a-b"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVLANRanges(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllocatableDropsReserved(t *testing.T) {
	got, err := allocatable([]string{"101-105"}, []int32{102, 104, 300})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int32{101, 103, 105}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFirstFreeIsLowestAndDeterministic(t *testing.T) {
	candidates := []int32{101, 102, 103}
	used := vlanSet{}
	used.add(101)
	for range 3 {
		id, err := firstFree(candidates, used)
		if err != nil || id != 102 {
			t.Fatalf("got %d, %v; want 102", id, err)
		}
	}
	used.add(102)
	used.add(103)
	if _, err := firstFree(candidates, used); !errors.Is(err, errZoneExhausted) {
		t.Fatalf("want errZoneExhausted, got %v", err)
	}
	if n := countFree(candidates, used); n != 0 {
		t.Fatalf("countFree = %d, want 0", n)
	}
}
