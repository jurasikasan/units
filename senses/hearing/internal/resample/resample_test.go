package resample

import "testing"

func Test8kLinearInterpolation(t *testing.T) {
	r := &Resampler{}
	r.SetRate(8000)
	got := r.Push([]int16{10, 20, 30})
	want := []int16{10, 15, 20, 25, 30}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got = %v, want %v", got, want)
		}
	}
	// Continuity across packets: the interpolation between 30 and 40
	// must still appear.
	got = r.Push([]int16{40})
	want = []int16{35, 40}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got = %v, want %v", got, want)
	}
	// [10,15,20,25,30] plus [35,40]: every sample plus the midpoint
	// between consecutive samples.
	if r.Emitted() != 7 {
		t.Fatalf("Emitted = %d, want 7", r.Emitted())
	}
}

func Test48kAverageEveryThree(t *testing.T) {
	r := &Resampler{}
	r.SetRate(48000)
	got := r.Push([]int16{1, 2, 3, 4, 5})
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("got = %v, want [2]", got)
	}
	// Two samples are carried over to the next packet: the next
	// average is (4+5+7)/3 = 5, and [8,9] are carried again.
	got = r.Push([]int16{7, 8, 9})
	if len(got) != 1 || got[0] != 5 {
		t.Fatalf("got = %v, want [5]", got)
	}
	if r.Emitted() != 2 {
		t.Fatalf("Emitted = %d, want 2", r.Emitted())
	}
}
