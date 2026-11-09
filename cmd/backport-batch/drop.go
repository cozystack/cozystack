/*
Copyright 2026 The Cozystack Authors.

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

package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// A drop is a maintainer's decision that an original stays off one line,
// for a reason, where the rules would take it. It is kept in the line's
// record and carried from batch to batch, bound to the original's merge
// commit on main, so a record cannot point it at another original. It is
// written only to the open batch's record, like everything else there.

// onLine says whether the line carries v, whatever rule decided it.
func (b *batch) onLine(v *verdict) (bool, error) {
	if v.rule == ruleOnLine {
		return true, nil
	}
	if err := b.load(v); err != nil {
		return false, err
	}
	how, err := b.lineHist.carries(v.o, v.commits)
	return how != "", err
}

// carried says whether the open batch has a unit of v, by the mapping the
// rebuild takes units off with: found needs every commit of a merged
// original, and one whose pick came out empty is missing from it.
func (b *batch) carried(v *verdict) (bool, error) {
	units, err := batchUnits(b.prHead, b.lineHead)
	if err != nil {
		return false, err
	}
	for _, u := range units {
		if o := b.unitOriginal(u); o != nil && o.o.number == v.o.number {
			return true, nil
		}
	}
	return false, nil
}

// redrop drops the originals in numbers from the line, for reason, or with
// undrop withdraws their drops. What the open batch carries of a dropped
// original comes off it, as --defer takes it off; otherwise only the record
// changes, and the batch stays as it is even when the line moved since.
func (a *app) redrop(b *batch, numbers []int, reason string, undrop bool, push bool) int {
	if !b.src.open {
		return a.fail(fmt.Errorf("no open batch pull request on %s carries a record; a drop is recorded on one", b.line.branch()))
	}
	drops := map[int]droppedEntry{}
	for _, d := range b.src.rec.Dropped {
		drops[d.Original] = d
	}
	mode, today := modeDrop, a.now().UTC().Format(time.DateOnly)
	if undrop {
		mode = modeUndrop
	}
	for _, n := range numbers {
		v := b.find(n)
		_, isDropped := drops[n]
		switch {
		case undrop && !isDropped:
			return a.fail(fmt.Errorf("#%d is not dropped from %s", n, b.line.branch()))
		case undrop:
			delete(drops, n)
		case v == nil:
			return a.fail(fmt.Errorf("#%d did not merge into main after %s branched", n, b.line.branch()))
		default:
			// backport/skip and rules 1 to 4 decide before the line is
			// looked at, so whether it carries the original is asked here.
			on, err := b.onLine(v)
			if err != nil {
				return a.fail(err)
			}
			if on {
				return a.fail(fmt.Errorf("#%d is already on %s; a drop cannot remove code the line has", n, b.line.branch()))
			}
			drops[n] = droppedEntry{Original: n, Merge: v.o.merge, Reason: reason, On: b.pr.Number, Date: today}
		}
	}
	active := map[int]droppedEntry{}
	for n, d := range drops {
		if b.find(n) != nil {
			active[n] = d
		}
	}
	b.drops = active
	out := map[int]string{}
	for _, n := range numbers {
		v := b.find(n)
		if v == nil {
			continue
		}
		if err := b.judge(v, active); err != nil {
			return a.fail(err)
		}
		if err := b.load(v); err != nil {
			return a.fail(err)
		}
		if undrop {
			continue
		}
		on, err := b.carried(v)
		if err != nil {
			return a.fail(err)
		}
		if on {
			out[n] = "dropped"
		}
	}
	hold := map[int]string{}
	for n, e := range b.recorded() {
		if e.Reason != "" {
			hold[n] = e.Reason
		}
	}
	list := slices.SortedFunc(maps.Values(drops), func(x, y droppedEntry) int { return cmp.Compare(x.Original, y.Original) })
	if len(out) > 0 {
		maps.Copy(out, hold)
		return a.reassemble(b, out, hold, list, mode, push)
	}
	if err := a.warnReset(b.pr.Head.Ref, b.prHead); err != nil {
		return a.fail(err)
	}
	if _, err := git("switch", "--quiet", "--no-track", "-C", b.pr.Head.Ref, "--end-of-options", b.prHead); err != nil {
		return a.fail(err)
	}
	return a.reclassify(b, b.picks, hold, list, mode, push)
}

// bindDrops holds each drop the record keeps to the original it names: a
// drop of an original merged since the line branched has to name that
// original's merge commit, and one of an earlier original a merge of it on
// main's first-parent line. The first apply; the others are kept and carried
// on, and decide nothing.
func (b *batch) bindDrops(merges []*original) (map[int]droppedEntry, error) {
	return bindEntries(b.src.rec.dropped(), merges, b.mainHead, func(d droppedEntry) string {
		return fmt.Sprintf("batch pull request #%d drops #%d by %.12s", b.src.carrier.Number, d.Original, d.Merge)
	})
}

// bindEntries binds drops to main: one whose original is among merges, the
// originals main merged since the line branched, has to name that
// original's merge exactly; any other has to be a merge of its number on
// main's first-parent line up to mainHead, as an inherited drop is. It
// returns the drops whose originals are among merges; about says what an
// error is about.
func bindEntries(drops []droppedEntry, merges []*original, mainHead string, about func(droppedEntry) string) (map[int]droppedEntry, error) {
	byMerge, byNumber := map[string]*original{}, map[int]*original{}
	for _, o := range merges {
		byMerge[o.merge], byNumber[o.number] = o, o
	}
	active := map[int]droppedEntry{}
	for _, d := range drops {
		at := about(d)
		if o := byMerge[d.Merge]; o != nil {
			if o.number != d.Original {
				return nil, fmt.Errorf("%s, which merged #%d", at, o.number)
			}
			active[d.Original] = d
			continue
		}
		if o := byNumber[d.Original]; o != nil {
			return nil, fmt.Errorf("%s, but #%d merged as %.12s", at, d.Original, o.merge)
		}
		ok, err := mergedOnMain(d.Merge, mainHead, d.Original)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s, which is no merge of it on main", at)
		}
	}
	return active, nil
}

// bindDrops binds each drop a report keeps to main exactly as the record's
// drops are bound, over the window the report classified: main and the
// line at the heads it was made from. Its errors read after "report <id>".
func (d *reportDoc) bindDrops() error {
	if len(d.Overlays.Dropped) == 0 {
		return nil
	}
	for _, h := range []struct{ name, head string }{{"main", d.Built.Main}, {d.Line, d.Built.Line}} {
		have, err := lookup(h.head)
		if err != nil {
			return err
		}
		if have == "" {
			return fmt.Errorf("was made on %s at %.12s, which this clone does not have; fetch %s and run again", h.name, h.head, h.name)
		}
	}
	out, err := git("merge-base", "--end-of-options", d.Built.Main, d.Built.Line)
	if err != nil {
		return fmt.Errorf("was made from a line that shares no history with main: %w", err)
	}
	merges, _, err := mainMerges(strings.TrimSpace(out), d.Built.Main)
	if err != nil {
		return err
	}
	active, err := bindEntries(d.Overlays.Dropped, merges, d.Built.Main, func(e droppedEntry) string {
		return fmt.Sprintf("drops #%d by %.12s", e.Original, e.Merge)
	})
	if err != nil {
		return err
	}
	// A drop decides only for an original the report classified, and only
	// once every earlier rule has passed it by; an inherited one decides
	// nothing.
	where, err := d.categories()
	if err != nil {
		return err
	}
	for _, n := range d.Primary.Skipped.Dropped {
		if _, ok := active[n]; !ok {
			return fmt.Errorf("has #%d dropped, but no drop of it binds to an original it classified", n)
		}
	}
	for n := range active {
		switch c := where[n]; c {
		case "skipped.dropped", "skipped.skip-label", "skipped.before-start", "skipped.type", "skipped.breaking", "skipped.touches", "skipped.on-line":
		default:
			return fmt.Errorf("drops #%d, which it has %s, though a drop decides before that", n, or(c, "nowhere"))
		}
	}
	return nil
}

// mergedOnMain says whether merge is on main's first-parent line up to
// mainHead and its message names pull request n, as mainMerges reads one.
func mergedOnMain(merge, mainHead string, n int) (bool, error) {
	have, err := lookup(merge)
	if err != nil || have == "" {
		return false, err
	}
	if ok, err := isAncestor(merge, mainHead); err != nil || !ok {
		return false, err
	}
	line, err := revList("--first-parent", "--end-of-options", mainHead)
	if err != nil || !slices.Contains(line, merge) {
		return false, err
	}
	msgs, err := commitMessages([]string{merge})
	if err != nil {
		return false, err
	}
	number, _, _ := mergedPR(msgs[merge].message)
	return number == n, nil
}
