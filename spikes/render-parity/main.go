// Renders cases.json (name -> Markdown) to go.json (name -> HTML) with the
// Markdown library the server would use, configured to follow the same rules
// as the front end: GFM plus the CJK-friendly emphasis and strikethrough rule.
//
//	GOWORK=off go run .            # with the CJK-friendly extension
//	GOWORK=off go run . -plain     # stock goldmark, for comparison
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	cjkfriendly "github.com/tats-u/goldmark-cjk-friendly/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

func main() {
	plain := flag.Bool("plain", false, "stock goldmark without the CJK-friendly extension")
	flag.Parse()

	exts := []goldmark.Extender{extension.Table, extension.TaskList, extension.Linkify}
	if *plain {
		exts = append(exts, extension.Strikethrough)
	} else {
		exts = append(exts, cjkfriendly.CJKFriendlyEmphasisAndStrikethrough)
	}
	md := goldmark.New(goldmark.WithExtensions(exts...), goldmark.WithRendererOptions(html.WithUnsafe()))

	data, err := os.ReadFile("cases.json")
	if err != nil {
		panic(err)
	}
	var cases map[string]string
	if err := json.Unmarshal(data, &cases); err != nil {
		panic(err)
	}
	out := make(map[string]string, len(cases))
	for name, src := range cases {
		var buf bytes.Buffer
		if err := md.Convert([]byte(src), &buf); err != nil {
			panic(err)
		}
		out[name] = buf.String()
	}
	data, _ = json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile("go.json", data, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("rendered %d cases\n", len(out))
}
