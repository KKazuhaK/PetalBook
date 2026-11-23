package links

import (
	"strings"
	"testing"
)

// benchPage is a page of about 40 KB: paragraphs of prose with links,
// emphasis, a list, a table and a code block.
func benchPage() []byte {
	var b strings.Builder
	b.WriteString("---\nid: 01JD3K8Q00\ntitle: T\n---\n")
	for i := 0; i < 60; i++ {
		b.WriteString("## 小节\n\n暮雪纷纷，见[暮雪](../Tang/暮雪.md \"01JD3K8Q7Z\")和 **粗体** 以及 *斜体*，还有 https://example.com/x 与 www.example.com。")
		b.WriteString("Some English text with a [link](b.md) and `code` and more words to fill the line.\n\n")
		b.WriteString("- item [c](c.md)\n- item two\n\n| a | b |\n|---|---|\n| [x](x.md) | y |\n\n```\ncode [no](no.md)\n```\n\n")
	}
	return []byte(b.String())
}

func BenchmarkExtract(b *testing.B) {
	data := benchPage()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Extract("Notes/a.md", data)
	}
}
