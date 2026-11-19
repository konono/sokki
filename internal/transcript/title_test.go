package transcript

import "testing"

func TestTitleSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"予算見直しの議論", "予算見直しの議論"},
		{"  Budget  review\n", "Budget_review"},
		{"a/b\\c:d*e?f\"g<h>i|j", "a_b_c_d_e_f_g_h_i_j"},
		{"..隠し", "隠し"},
		{"\x00\x1f", ""},
		{"", ""},
		{"あいうえおかきくけこさしすせそたちつてとなにぬねのはひふへほまみむめもやゆよらりるれろ", "あいうえおかきくけこさしすせそたちつてとなにぬねのはひふへほまみむめもやゆよらり"},
	}
	for _, c := range cases {
		if got := TitleSlug(c.in); got != c.want {
			t.Errorf("TitleSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
