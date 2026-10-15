package backup

import "testing"

// Граница Р12: меньше половины прошлой удачной и прошлая — от мегабайта.
func TestShrunkBoundaries(t *testing.T) {
	const mb = 1 << 20
	cases := []struct {
		previous, current int64
		want              bool
	}{
		{120 * mb, 40 * mb, true},
		{100 * mb, 50 * mb, false}, // ровно половина — ещё не повод
		{100 * mb, 50*mb - 1, true},
		{mb, mb/2 - 1, true},
		{mb - 1, 1, false},    // прошлая меньше мегабайта — шум
		{50 * 1024, 1, false}, // пустой сайт
		{100 * mb, 0, false},  // размера нет — не знаем, а не «похудела»
		{0, 10 * mb, false},
	}
	for _, tc := range cases {
		if got := Shrunk(tc.previous, tc.current); got != tc.want {
			t.Errorf("Shrunk(%d, %d) = %v", tc.previous, tc.current, got)
		}
	}
}

// Пометка — у удачной копии против прошлой удачной; неудачные круги между
// ними не в счёт, самый старый в списке сравнивать не с чем.
func TestMarkShrunkSkipsFailedRounds(t *testing.T) {
	const mb = 1 << 20
	runs := []Record{
		{OK: true, DumpBytes: 30 * mb},
		{OK: false},
		{OK: true, DumpBytes: 100 * mb},
		{OK: true, DumpBytes: 100 * mb},
		{OK: true, DumpBytes: 5 * mb},
	}
	MarkShrunk(runs)
	if runs[0].ShrunkFrom != 100*mb {
		t.Errorf("новая копия: %d", runs[0].ShrunkFrom)
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].ShrunkFrom != 0 {
			t.Errorf("круг %d помечен: %d", i, runs[i].ShrunkFrom)
		}
	}
}
