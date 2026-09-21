package fx

import "testing"

func TestPartsNeverOverallocateOrLeaveNegativeRemainder(t *testing.T) {
	for total := int64(1); total <= 30; total++ {
		for eur := int64(0); eur <= 30; eur++ {
			r := Record{OriginalMinor: total, EURMinor: eur, Source: SourceECB}
			var allocated int64
			for prior := int64(0); prior < total; prior++ {
				part, err := r.Part(1, prior, allocated)
				if err != nil || part.EURMinor < 0 || allocated+part.EURMinor > eur {
					t.Fatalf("%d/%d, prior=%d EUR=%d: part=%+v error=%v", total, eur, prior, allocated, part, err)
				}
				allocated += part.EURMinor
			}
			if allocated != eur {
				t.Fatalf("allocated %d, expected %d", allocated, eur)
			}
		}
	}
}
