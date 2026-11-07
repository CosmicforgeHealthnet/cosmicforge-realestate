package reservations

import "testing"

func slotAmounts(amount int64, quantity int) []int64 {
	out := make([]int64, quantity)
	base, extra := amount/int64(quantity), amount%int64(quantity)
	for i := range out {
		out[i] = base
		if int64(i) < extra {
			out[i]++
		}
	}
	return out
}

func TestTailPartitionPreservesEverySlotAndConservesAmount(t *testing.T) {
	for _, amount := range []int64{0, 1, 2, 7, 10, 59999, 60000, 60001, 99999} {
		for q := 1; q <= 9; q++ {
			for k := 1; k <= q; k++ {
				tail, remaining, err := TailPartition(amount, q, k)
				if err != nil {
					t.Fatal(err)
				}
				if tail+remaining != amount || tail < 0 || remaining < 0 {
					t.Fatalf("not conserved amount=%d q=%d k=%d tail=%d remaining=%d", amount, q, k, tail, remaining)
				}
				slots := slotAmounts(amount, q)
				var want int64
				for _, v := range slots[q-k:] {
					want += v
				}
				if tail != want {
					t.Fatalf("tail amount=%d q=%d k=%d got=%d want=%d", amount, q, k, tail, want)
				}
				if k < q {
					// The remaining line re-partitions to exactly the original first q-k slots.
					rest := slotAmounts(remaining, q-k)
					for i, v := range rest {
						if v != slots[i] {
							t.Fatalf("remaining slot %d changed amount=%d q=%d k=%d: %d != %d", i+1, amount, q, k, v, slots[i])
						}
					}
				}
			}
		}
	}
	for _, bad := range [][3]int64{{-1, 2, 1}, {10, 0, 1}, {10, 2, 0}, {10, 2, 3}} {
		if _, _, err := TailPartition(bad[0], int(bad[1]), int(bad[2])); err == nil {
			t.Fatalf("invalid partition accepted %v", bad)
		}
	}
}
