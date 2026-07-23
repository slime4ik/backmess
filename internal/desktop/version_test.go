package desktop

import "testing"

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.2.0", "0.1.9", false},
		{"0.2.0", "0.10.0", true},
		{"1.0.0", "0.9.9", false},
		{"0.2.0", "0.2.1", true},
		{"0.2", "0.2.0", false},
		{"0.2.0", "0.2.0-rc1", false}, // rc-суффикс отбрасывается
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q,%q)=%v, хотели %v", c.a, c.b, got, c.want)
		}
	}
}
