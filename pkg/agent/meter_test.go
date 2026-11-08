package agent

import "testing"

// The meter belongs to the conversation: a request goes on from where the
// last one left it, and only an announced compaction lowers it.
func TestTheMeterGoesOnFromTheLastRequest(t *testing.T) {
	var shown []int
	var compacts [][3]int
	l := &EventListener{
		OnContext: func(used, window int) { shown = append(shown, used) },
		OnCompact: func(before, after, window int, summarized bool) {
			s := 0
			if summarized {
				s = 1
			}
			compacts = append(compacts, [3]int{before, after, s})
		},
	}
	m := &contextMeter{window: 256000, carry: 20000, input: 100}
	m.report(l, 7000) // what is sent: the instructions, the history and the new message
	m.report(l, 9000) // a tool result came back
	if shown[0] != 20100 || shown[1] != 22100 {
		t.Fatalf("the meter starts where the conversation was and grows: %v", shown)
	}
	// At the threshold the earlier requests' outputs (no longer sent) go, announced.
	m.report(l, 180000)
	if len(compacts) != 1 || compacts[0] != [3]int{193100, 180000, 0} || shown[len(shown)-1] != 180000 {
		t.Fatalf("compacts %v, shown %v", compacts, shown)
	}
	// A compaction during the job lowers it to what is left.
	m.compacted(l, 190000, 90000, true)
	if shown[len(shown)-1] != 90000 {
		t.Errorf("after a compaction: %v", shown)
	}

	// The conversation was compacted between two requests: shown when the next one starts.
	shown, compacts = nil, nil
	m = &contextMeter{window: 256000, carry: 150000, carryCompacted: true, input: 100}
	m.report(l, 9000)
	if len(compacts) != 1 || compacts[0] != [3]int{150000, 9000, 1} || shown[0] != 9000 {
		t.Errorf("compacts %v, shown %v", compacts, shown)
	}

	// Nothing to carry: the context that is sent.
	shown = nil
	m = &contextMeter{window: 256000}
	m.report(l, 5000)
	if shown[0] != 5000 {
		t.Errorf("a first request: %v", shown)
	}
}
