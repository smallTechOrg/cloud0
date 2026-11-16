package attribution

import (
	"fmt"
	"sort"
)

// NewAllocator returns the pure shared-cost Allocator (see the Allocator
// contract in types.go).
func NewAllocator() Allocator { return allocator{} }

type allocator struct{}

// NoEligibleNote is the note returned when shared cost cannot be placed.
const NoEligibleNote = "No products to allocate shared cost to."

func cloneGroups(in []Group) []Group {
	out := make([]Group, len(in))
	for i, g := range in {
		c := g
		c.Cells = make(map[string]BreakdownCell, len(g.Cells))
		for k, v := range g.Cells {
			c.Cells[k] = v
		}
		c.RuleIDs = append([]int64{}, g.RuleIDs...)
		out[i] = c
	}
	return out
}

func (allocator) Allocate(groups []Group, mode AllocateMode) ([]Group, []string, error) {
	if !mode.Valid() {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidAllocate, string(mode))
	}
	out := cloneGroups(groups)
	notes := []string{}
	if mode == AllocateNone {
		return out, notes, nil
	}
	shared := -1
	var eligible []int
	for i, g := range out {
		switch {
		case g.Kind == KindShared && shared < 0:
			shared = i
		case (g.Kind == KindProduct || g.Kind == KindEnvironment) && g.DirectNet != 0:
			eligible = append(eligible, i)
		}
	}
	if shared < 0 {
		return out, notes, nil
	}
	sg := out[shared]
	if sg.DirectNet == 0 && !anyCell(sg.Cells) {
		return out, notes, nil
	}
	if len(eligible) == 0 {
		return out, append(notes, NoEligibleNote), nil
	}

	weights := make([]float64, len(eligible))
	effective := mode
	var sum float64
	for j, i := range eligible {
		sum += out[i].DirectNet
		weights[j] = out[i].DirectNet
	}
	if mode == AllocateProportional && sum == 0 {
		effective = AllocateEven
		notes = append(notes, "Direct costs sum to zero; shared cost was split evenly instead of proportionally.")
	}
	if effective == AllocateEven {
		for j := range weights {
			weights[j] = 1 / float64(len(eligible))
		}
	} else {
		for j := range weights {
			weights[j] /= sum
		}
	}

	keys := make([]string, 0, len(sg.Cells))
	for k := range sg.Cells {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sc := sg.Cells[k]
		var remN, remG, remC = sc.Net, sc.Gross, sc.Credits
		for j, i := range eligible {
			var add BreakdownCell
			if j == len(eligible)-1 { // remainder: conserves the total exactly
				add = BreakdownCell{Net: remN, Gross: remG, Credits: remC}
			} else {
				add = BreakdownCell{Net: sc.Net * weights[j], Gross: sc.Gross * weights[j], Credits: sc.Credits * weights[j]}
				remN, remG, remC = remN-add.Net, remG-add.Gross, remC-add.Credits
			}
			c := out[i].Cells[k]
			c.Net += add.Net
			c.Gross += add.Gross
			c.Credits += add.Credits
			out[i].Cells[k] = c
			if k == ColumnTotal {
				out[i].AllocatedNet = add.Net
			}
		}
	}
	for _, i := range eligible {
		out[i].Net = out[i].DirectNet + out[i].AllocatedNet
	}
	out[shared].AllocatedNet = -sg.DirectNet
	out[shared].Net = 0
	out[shared].Cells = map[string]BreakdownCell{ColumnTotal: {}}

	unit := "products"
	if out[eligible[0]].Kind == KindEnvironment {
		unit = "environments"
	}
	how := "evenly"
	if effective == AllocateProportional {
		how = "proportionally to direct cost"
	}
	notes = append(notes, fmt.Sprintf("Shared cost %.2f was allocated %s across %d %s.", sg.DirectNet, how, len(eligible), unit))
	return out, notes, nil
}

func anyCell(cells map[string]BreakdownCell) bool {
	for _, c := range cells {
		if c != (BreakdownCell{}) {
			return true
		}
	}
	return false
}
