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
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"testing"
)

// fakeNB is an in-memory OVN_Northbound with the three tables the gateway
// code touches and the OVSDB semantics it relies on: transactions are atomic,
// named-uuids resolve within a transaction, strong references must resolve at
// commit, HA_Chassis_Group names are unique, and HA_Chassis rows no group
// references are garbage-collected (the table is not a root table). Rows go
// out in wire encoding, single-element sets as bare atoms, as ovsdb-server
// writes them.
type fakeNB struct {
	mu     sync.Mutex
	tables map[string]map[string]fakeRow
	nextID int
	// err, when set, fails every transaction as a transport error would.
	err error
	// writes counts committed transactions that changed something.
	writes int
	// waitsFailed counts transactions a wait operation aborted.
	waitsFailed int
}

// projectRow is the row's columns in a printable, comparable order.
func projectRow(r fakeRow, cols []string) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, fmt.Sprintf("%s=%v", c, r[c]))
	}
	return out
}

type fakeRow map[string]any

const (
	kindString = "string"
	kindInt    = "int"
	kindUUIDs  = "uuids"
	kindMap    = "map"
)

var fakeSchema = map[string]map[string]string{
	tableLRP:          {colName: kindString, colHAChassisGroup: kindUUIDs, colGatewayChassis: kindUUIDs, colExternalIDs: kindMap},
	tableHAChassisGrp: {colName: kindString, colHAChassis: kindUUIDs, colExternalIDs: kindMap},
	tableHAChassis:    {colChassisName: kindString, colPriority: kindInt, colExternalIDs: kindMap},
}

func newFakeNB() *fakeNB {
	f := &fakeNB{tables: map[string]map[string]fakeRow{}}
	for t := range fakeSchema {
		f.tables[t] = map[string]fakeRow{}
	}
	return f
}

func (f *fakeNB) newUUID() string {
	f.nextID++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextID)
}

func emptyValue(kind string) any {
	switch kind {
	case kindString:
		return ""
	case kindInt:
		return 0
	case kindUUIDs:
		return []string{}
	default:
		return map[string]string{}
	}
}

func cloneRow(r fakeRow) fakeRow {
	out := fakeRow{}
	for k, v := range r {
		switch v := v.(type) {
		case []string:
			out[k] = slices.Clone(v)
		case map[string]string:
			out[k] = maps.Clone(v)
		default:
			out[k] = v
		}
	}
	return out
}

func (f *fakeNB) snapshot() map[string]map[string]fakeRow {
	out := map[string]map[string]fakeRow{}
	for t, rows := range f.tables {
		out[t] = map[string]fakeRow{}
		for u, r := range rows {
			out[t][u] = cloneRow(r)
		}
	}
	return out
}

// normalize turns a value of an operation into the stored form of kind.
func normalize(kind string, v any, named map[string]string) (any, error) {
	switch kind {
	case kindString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("want string, got %T", v)
		}
		return s, nil
	case kindInt:
		n, ok := v.(int)
		if !ok {
			return nil, fmt.Errorf("want int, got %T", v)
		}
		return n, nil
	case kindUUIDs:
		var elems []any
		if s, ok := v.(ovsSet); ok {
			elems = s
		} else {
			elems = []any{v}
		}
		out := []string{}
		for _, e := range elems {
			switch e := e.(type) {
			case ovsUUID:
				out = append(out, string(e))
			case ovsNamedUUID:
				u, ok := named[string(e)]
				if !ok {
					return nil, fmt.Errorf("unknown named-uuid %q", e)
				}
				out = append(out, u)
			default:
				return nil, fmt.Errorf("want uuid, got %T", e)
			}
		}
		sort.Strings(out)
		return slices.Compact(out), nil
	case kindMap:
		m, ok := v.(ovsMap)
		if !ok {
			return nil, fmt.Errorf("want map, got %T", v)
		}
		return maps.Clone(map[string]string(m)), nil
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}

func (f *fakeNB) matches(table, uuid string, row fakeRow, where []ovsdbCondition) (bool, error) {
	for _, c := range where {
		col, fn := c[0].(string), c[1].(string)
		if col == colUUID {
			u, ok := c[2].(ovsUUID)
			if !ok || fn != "==" {
				return false, fmt.Errorf("unsupported _uuid condition %v", c)
			}
			if string(u) != uuid {
				return false, nil
			}
			continue
		}
		kind, ok := fakeSchema[table][col]
		if !ok {
			return false, fmt.Errorf("no column %s in %s", col, table)
		}
		want, err := normalize(kind, c[2], nil)
		if err != nil {
			return false, err
		}
		have := row[col]
		switch fn {
		case "==":
			if fmt.Sprint(have) != fmt.Sprint(want) {
				return false, nil
			}
		case "includes":
			switch kind {
			case kindMap:
				for k, v := range want.(map[string]string) {
					if have.(map[string]string)[k] != v {
						return false, nil
					}
				}
			case kindUUIDs:
				for _, u := range want.([]string) {
					if !slices.Contains(have.([]string), u) {
						return false, nil
					}
				}
			default:
				return false, fmt.Errorf("includes on %s", kind)
			}
		default:
			return false, fmt.Errorf("unsupported function %q", fn)
		}
	}
	return true, nil
}

func encodeValue(kind string, v any) json.RawMessage {
	var out any
	switch kind {
	case kindUUIDs:
		s := ovsSet{}
		for _, u := range v.([]string) {
			s = append(s, ovsUUID(u))
		}
		out = s
	case kindMap:
		out = ovsMap(v.(map[string]string))
	default:
		out = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return b
}

// Transact implements ovsdbTransactor.
func (f *fakeNB) Transact(_ context.Context, ops ...ovsdbOp) ([]ovsdbResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	before := f.snapshot()
	nextID := f.nextID
	named := map[string]string{}
	res := make([]ovsdbResult, len(ops))
	fail := func(i int, err error) ([]ovsdbResult, error) {
		f.tables, f.nextID = before, nextID
		res[i] = ovsdbResult{Error: "syntax error", Details: err.Error()}
		return res, nil
	}
	changed := false
	for i, op := range ops {
		rows, ok := f.tables[op.Table]
		if !ok {
			return fail(i, fmt.Errorf("unknown table %s", op.Table))
		}
		schema := fakeSchema[op.Table]
		matching := func() ([]string, error) {
			var out []string
			for u, r := range rows {
				ok, err := f.matches(op.Table, u, r, op.Where)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, u)
				}
			}
			sort.Strings(out)
			return out, nil
		}
		setColumns := func(r fakeRow, values map[string]any) error {
			for col, v := range values {
				kind, ok := schema[col]
				if !ok {
					return fmt.Errorf("no column %s in %s", col, op.Table)
				}
				n, err := normalize(kind, v, named)
				if err != nil {
					return fmt.Errorf("%s: %w", col, err)
				}
				r[col] = n
			}
			return nil
		}
		switch op.Op {
		case "select":
			uuids, err := matching()
			if err != nil {
				return fail(i, err)
			}
			cols := op.Columns
			if cols == nil {
				cols = append([]string{colUUID}, slices.Sorted(maps.Keys(schema))...)
			}
			for _, u := range uuids {
				out := ovsdbRow{}
				for _, col := range cols {
					if col == colUUID {
						b, _ := json.Marshal(ovsUUID(u))
						out[col] = b
						continue
					}
					out[col] = encodeValue(schema[col], rows[u][col])
				}
				res[i].Rows = append(res[i].Rows, out)
			}
		case "insert":
			r := fakeRow{}
			for col, kind := range schema {
				r[col] = emptyValue(kind)
			}
			u := f.newUUID()
			if op.UUIDName != "" {
				named[op.UUIDName] = u
			}
			if err := setColumns(r, op.Row); err != nil {
				return fail(i, err)
			}
			rows[u] = r
			id := ovsUUID(u)
			res[i].UUID = &id
			changed = true
		case "update":
			uuids, err := matching()
			if err != nil {
				return fail(i, err)
			}
			for _, u := range uuids {
				if err := setColumns(rows[u], op.Row); err != nil {
					return fail(i, err)
				}
			}
			res[i].Count = len(uuids)
			changed = changed || len(uuids) > 0
		case "mutate":
			uuids, err := matching()
			if err != nil {
				return fail(i, err)
			}
			for _, u := range uuids {
				for _, m := range op.Mutations {
					col, mut := m[0].(string), m[1].(string)
					if schema[col] != kindUUIDs {
						return fail(i, fmt.Errorf("mutate on %s", col))
					}
					v, err := normalize(kindUUIDs, m[2], named)
					if err != nil {
						return fail(i, err)
					}
					have := rows[u][col].([]string)
					switch mut {
					case "insert":
						have = append(have, v.([]string)...)
						sort.Strings(have)
						have = slices.Compact(have)
					case "delete":
						have = slices.DeleteFunc(have, func(s string) bool { return slices.Contains(v.([]string), s) })
					default:
						return fail(i, fmt.Errorf("mutator %q", mut))
					}
					rows[u][col] = have
				}
			}
			res[i].Count = len(uuids)
			changed = changed || len(uuids) > 0
		case "wait":
			// Only timeout 0 is sent: a wait that does not hold aborts the
			// transaction, as ovsdb-server answers "timed out".
			uuids, err := matching()
			if err != nil {
				return fail(i, err)
			}
			var have, want []string
			for _, u := range uuids {
				have = append(have, fmt.Sprint(projectRow(rows[u], op.Columns)))
			}
			for _, w := range op.WaitRows {
				r := fakeRow{}
				for col, v := range w {
					n, err := normalize(schema[col], v, named)
					if err != nil {
						return fail(i, err)
					}
					r[col] = n
				}
				want = append(want, fmt.Sprint(projectRow(r, op.Columns)))
			}
			sort.Strings(have)
			sort.Strings(want)
			if (op.Until == "==") != slices.Equal(have, want) {
				f.tables, f.nextID = before, nextID
				res[i] = ovsdbResult{Error: "timed out"}
				f.waitsFailed++
				return res, nil
			}
		case "delete":
			uuids, err := matching()
			if err != nil {
				return fail(i, err)
			}
			for _, u := range uuids {
				delete(rows, u)
			}
			res[i].Count = len(uuids)
			changed = changed || len(uuids) > 0
		default:
			return fail(i, fmt.Errorf("unsupported op %q", op.Op))
		}
	}
	// Commit: unique names, strong references, garbage collection.
	names := map[string]bool{}
	for _, g := range f.tables[tableHAChassisGrp] {
		n := g[colName].(string)
		if names[n] {
			f.tables, f.nextID = before, nextID
			return append(res, ovsdbResult{Error: "constraint violation", Details: "duplicate HA_Chassis_Group name " + n}), nil
		}
		names[n] = true
		for _, c := range g[colHAChassis].([]string) {
			if _, ok := f.tables[tableHAChassis][c]; !ok {
				f.tables, f.nextID = before, nextID
				return append(res, ovsdbResult{Error: "referential integrity violation", Details: "HA_Chassis " + c}), nil
			}
		}
	}
	for _, p := range f.tables[tableLRP] {
		for _, g := range p[colHAChassisGroup].([]string) {
			if _, ok := f.tables[tableHAChassisGrp][g]; !ok {
				f.tables, f.nextID = before, nextID
				return append(res, ovsdbResult{Error: "referential integrity violation",
					Details: "cannot delete HA_Chassis_Group row " + g + " because of 1 remaining reference(s)"}), nil
			}
		}
	}
	referenced := map[string]bool{}
	for _, g := range f.tables[tableHAChassisGrp] {
		for _, c := range g[colHAChassis].([]string) {
			referenced[c] = true
		}
	}
	for u := range f.tables[tableHAChassis] {
		if !referenced[u] {
			delete(f.tables[tableHAChassis], u)
		}
	}
	if changed {
		f.writes++
	}
	return res, nil
}

// Test helpers. They bypass transactions, as ovn-nbctl would from outside.

func (f *fakeNB) addRouterPort(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.newUUID()
	f.tables[tableLRP][u] = fakeRow{colName: name, colHAChassisGroup: []string{}, colGatewayChassis: []string{}, colExternalIDs: map[string]string{}}
	return u
}

func (f *fakeNB) deleteRouterPort(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for u, r := range f.tables[tableLRP] {
		if r[colName] == name {
			delete(f.tables[tableLRP], u)
		}
	}
}

// addGroup adds a group with chassis name -> priority, and points the router
// ports at it.
func (f *fakeNB) addGroup(name string, ids map[string]string, chassis map[string]int, ports ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var refs []string
	for _, c := range slices.Sorted(maps.Keys(chassis)) {
		u := f.newUUID()
		f.tables[tableHAChassis][u] = fakeRow{colChassisName: c, colPriority: chassis[c], colExternalIDs: map[string]string{}}
		refs = append(refs, u)
	}
	sort.Strings(refs)
	g := f.newUUID()
	if ids == nil {
		ids = map[string]string{}
	}
	f.tables[tableHAChassisGrp][g] = fakeRow{colName: name, colHAChassis: refs, colExternalIDs: maps.Clone(ids)}
	for _, p := range ports {
		for _, r := range f.tables[tableLRP] {
			if r[colName] == p {
				r[colHAChassisGroup] = []string{g}
			}
		}
	}
	return g
}

type fakeGroup struct {
	UUID        string
	ExternalIDs map[string]string
	Chassis     map[string]int
}

func (f *fakeNB) group(name string) *fakeGroup {
	f.mu.Lock()
	defer f.mu.Unlock()
	for u, g := range f.tables[tableHAChassisGrp] {
		if g[colName] != name {
			continue
		}
		out := &fakeGroup{UUID: u, ExternalIDs: maps.Clone(g[colExternalIDs].(map[string]string)), Chassis: map[string]int{}}
		for _, c := range g[colHAChassis].([]string) {
			r := f.tables[tableHAChassis][c]
			out.Chassis[r[colChassisName].(string)] = r[colPriority].(int)
		}
		return out
	}
	return nil
}

// portGroup is the name of the group the router port points at, "" for none.
func (f *fakeNB) portGroup(t *testing.T, port string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.tables[tableLRP] {
		if r[colName] != port {
			continue
		}
		refs := r[colHAChassisGroup].([]string)
		if len(refs) == 0 {
			return ""
		}
		return f.tables[tableHAChassisGrp][refs[0]][colName].(string)
	}
	t.Fatalf("router port %q does not exist", port)
	return ""
}

func (f *fakeNB) clearPortGroup(port string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.tables[tableLRP] {
		if r[colName] == port {
			r[colHAChassisGroup] = []string{}
		}
	}
}

func (f *fakeNB) count(table string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tables[table])
}

func (f *fakeNB) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *fakeNB) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// pointPort points the router port at the group with that uuid.
func (f *fakeNB) pointPort(port, group string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.tables[tableLRP] {
		if r[colName] == port {
			r[colHAChassisGroup] = []string{group}
		}
	}
}

// setPortGatewayChassis gives the router port gateway_chassis rows; the fake
// does not model the Gateway_Chassis table, only the reference.
func (f *fakeNB) setPortGatewayChassis(port string, refs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.tables[tableLRP] {
		if r[colName] == port {
			r[colGatewayChassis] = slices.Clone(refs)
		}
	}
}

// dropGroup deletes the group by name, clears it from the router ports and
// takes its HA_Chassis rows with it, as "ovn-nbctl clear" and
// "ha-chassis-group-del" from outside would.
func (f *fakeNB) dropGroup(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for u, g := range f.tables[tableHAChassisGrp] {
		if g[colName] != name {
			continue
		}
		for _, c := range g[colHAChassis].([]string) {
			delete(f.tables[tableHAChassis], c)
		}
		delete(f.tables[tableHAChassisGrp], u)
		for _, p := range f.tables[tableLRP] {
			p[colHAChassisGroup] = slices.DeleteFunc(p[colHAChassisGroup].([]string), func(s string) bool { return s == u })
		}
	}
}

// addGroupRows adds a group whose HA_Chassis rows are given one by one, so
// two rows may name the same chassis.
func (f *fakeNB) addGroupRows(name string, ids map[string]string, chassis []gatewayChassis, ports ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var refs []string
	for _, c := range chassis {
		u := f.newUUID()
		f.tables[tableHAChassis][u] = fakeRow{colChassisName: c.Name, colPriority: c.Priority, colExternalIDs: map[string]string{}}
		refs = append(refs, u)
	}
	sort.Strings(refs)
	g := f.newUUID()
	f.tables[tableHAChassisGrp][g] = fakeRow{colName: name, colHAChassis: refs, colExternalIDs: maps.Clone(ids)}
	for _, p := range ports {
		for _, r := range f.tables[tableLRP] {
			if r[colName] == p {
				r[colHAChassisGroup] = []string{g}
			}
		}
	}
	return g
}

// hookNB runs before before each transaction it passes on, numbered from 1.
type hookNB struct {
	*fakeNB
	n      int
	before func(n int)
}

func (h *hookNB) Transact(ctx context.Context, ops ...ovsdbOp) ([]ovsdbResult, error) {
	h.n++
	if h.before != nil {
		h.before(h.n)
	}
	return h.fakeNB.Transact(ctx, ops...)
}
