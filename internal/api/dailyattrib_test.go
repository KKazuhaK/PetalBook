package api

import "testing"

func TestParseAttribution(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, text, author, source string }{
		{"人到情多情转薄，而今真个不多情。 —— 纳兰性德《山花子》", "人到情多情转薄，而今真个不多情。", "纳兰性德", "山花子"},
		{"The best way out is always through. — Robert Frost", "The best way out is always through.", "Robert Frost", ""},
		{"Life is short. -- Seneca", "Life is short.", "Seneca", ""},
		{"Be water. — Bruce Lee, Interview", "Be water.", "Bruce Lee", "Interview"},
		{"Not all those who wander are lost. — J. R. R. Tolkien", "Not all those who wander are lost.", "J. R. R. Tolkien", ""},
		{"Cogito. — René Descartes", "Cogito.", "René Descartes", ""},
		{"Know thyself. — Ludwig van Beethoven", "Know thyself.", "Ludwig van Beethoven", ""},
		{"学而时习之 —— 《论语》", "学而时习之", "", "论语"},
		{"学而时习之。 —— 孔子", "学而时习之。", "孔子", ""},
		{"今夜月明人尽望 —— 王建《十五夜望月寄杜郎中》", "今夜月明人尽望", "王建", "十五夜望月寄杜郎中"},
		{"To be, or not to be — William Shakespeare《Hamlet》", "To be, or not to be", "William Shakespeare", "Hamlet"},
		// The last mark counts, and only that.
		{"Wait — what? — Douglas Adams", "Wait — what?", "Douglas Adams", ""},
		{"  人生若只如初见。 —— 纳兰性德  ", "人生若只如初见。", "纳兰性德", ""},
		{"“知之为知之”」 —— 孔子", "“知之为知之”」", "孔子", ""},
		{"He said \"go\" — Someone Else", "He said \"go\"", "Someone Else", ""},
		// Not an attribution: the line is text only.
		{"I think — therefore I am", "I think — therefore I am", "", ""},
		{"He said -- \"no\" -- and left.", "He said -- \"no\" -- and left.", "", ""},
		{"a — b — c", "a — b — c", "", ""},
		// A dash inside a sentence: what is before it does not end one, and there is no 《source》.
		{"春眠不觉晓 —— 处处闻啼鸟", "春眠不觉晓 —— 处处闻啼鸟", "", ""},
		{"人生如梦 —— 一尊还酹江月", "人生如梦 —— 一尊还酹江月", "", ""},
		{"Never give up — Never", "Never give up — Never", "", ""},
		{"War — Peace and Love", "War — Peace and Love", "", ""},
		{"Life is short -- Seneca", "Life is short -- Seneca", "", ""},
		{"学而时习之 —— 孔子", "学而时习之 —— 孔子", "", ""},
		// But a 《source》 after it says it is an attribution.
		{"春眠不觉晓 —— 孟浩然《春晓》", "春眠不觉晓", "孟浩然", "春晓"},
		{"—— 孔子", "—— 孔子", "", ""},
		{"Hello — ", "Hello —", "", ""},
		{"well—known and so-called", "well—known and so-called", "", ""},
		{"读《论语》有感", "读《论语》有感", "", ""},
		{"他说 —— 我走了。然后就真的走了，再没回来", "他说 —— 我走了。然后就真的走了，再没回来", "", ""},
		{"Nothing — it was a long, long time ago", "Nothing — it was a long, long time ago", "", ""},
		{"x —— 《未完", "x —— 《未完", "", ""},
		{"x —— 某《书》又一些", "x —— 某《书》又一些", "", ""},
		{"Dashes -- and more dashes -- Anonymous Sources Said That Day And Then Some", "Dashes -- and more dashes -- Anonymous Sources Said That Day And Then Some", "", ""},
	} {
		got := parseAttribution(c.in)
		if got.Text != c.text || got.Author != c.author || got.Source != c.source {
			t.Errorf("%q: got %+v, want text %q author %q source %q", c.in, got, c.text, c.author, c.source)
		}
	}
}
