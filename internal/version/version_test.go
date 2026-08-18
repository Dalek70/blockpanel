package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.2", "1.2.0", 0},
		{"1.10.0", "1.9.0", 1},
		{"1.2.0-rc1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc1", 1},
		{"1.2.0-rc1", "1.2.0-rc2", -1},
		{"garbage", "1.0.0", -1},
		{"1.0.0", "garbage", 1},
		{"", "", 0},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNewerAndValid(t *testing.T) {
	if !Newer("1.0.1", "1.0.0") || Newer("1.0.0", "1.0.0") || Newer("0.9.9", "1.0.0") {
		t.Error("Newer misbehaves")
	}
	if !Valid("1.2.3") || !Valid("v1.2.3-rc1") || Valid("") || Valid("not.a.version") {
		t.Error("Valid misbehaves")
	}
	if !Valid(Current) {
		t.Errorf("Current %q must be a valid version", Current)
	}
}
