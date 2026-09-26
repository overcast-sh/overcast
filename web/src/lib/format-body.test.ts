import { describe, expect, it } from "vitest"
import { formatBodyForDisplay, MAX_FORMAT_BYTES } from "./format-body"

describe("formatBodyForDisplay", () => {
  it("does not indent after HTML void tags when the content type is HTML", () => {
    // Given: an S3-style HTML preview with void tags that never close.
    const html = "<html><head><meta charset=\"utf-8\"><link rel=\"icon\" href=\"/f.ico\"></head><body>Hi<br><img src=\"x.png\"><p>after</p></body></html>"

    const { text } = formatBodyForDisplay(html, "xml", "text/html")

    // Then: siblings of the void tags stay at the same depth instead of
    // staircase-indenting after every <meta>/<br>/<img>.
    expect(text).toBe(
      [
        "<html>",
        "  <head>",
        "    <meta charset=\"utf-8\">",
        "    <link rel=\"icon\" href=\"/f.ico\">",
        "  </head>",
        "  <body>",
        "    Hi",
        "    <br>",
        "    <img src=\"x.png\">",
        "    <p>",
        "      after",
        "    </p>",
        "  </body>",
        "</html>",
      ].join("\n"),
    )
  })

  it("still treats look-alike element names as containers in plain XML", () => {
    // Given: an XML document where <link> is an ordinary element with a close tag.
    const xml = "<feed><link><href>x</href></link></feed>"

    const { text } = formatBodyForDisplay(xml, "xml", "application/xml")

    expect(text).toBe(
      ["<feed>", "  <link>", "    <href>", "      x", "    </href>", "  </link>", "</feed>"].join("\n"),
    )
  })

  it("honours an explicit htmlVoidTags override without a content type", () => {
    const { text } = formatBodyForDisplay("<div><br><span>x</span></div>", "xml", undefined, {
      htmlVoidTags: true,
    })

    expect(text).toBe(
      ["<div>", "  <br>", "  <span>", "    x", "  </span>", "</div>"].join("\n"),
    )
  })

  it("returns oversized bodies as plain text without formatting or highlighting", () => {
    // Given: a JSON body just over the pretty-print threshold.
    const big = `{"pad":"${"x".repeat(MAX_FORMAT_BYTES)}"}`

    const result = formatBodyForDisplay(big, "json", "application/json")

    // Then: the raw text comes back untouched and no HTML is produced.
    expect(result.text).toBe(big)
    expect(result.html).toBeUndefined()
  })

  it("still pretty-prints bodies under the threshold", () => {
    const result = formatBodyForDisplay('{"a":1}', "json", "application/json")

    expect(result.text).toBe('{\n  "a": 1\n}')
    expect(result.html).toBeDefined()
  })
})
