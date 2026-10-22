# Evidence placement and validation

## Link shape

Use the versioned link-only format:

```text
papers-manager://self/evidence?v=1&page=5&rect=0.12,0.25,0.86,0.48&block=<uuid>&section=3
```

All parameters:

| Parameter | Required | Meaning |
| --- | --- | --- |
| `v` | yes | Format version; currently `1`. |
| `page` | yes | 1-based PDF page number. |
| `rect` | no | Normalized page rectangle; may repeat. Values are 0–1. |
| `block` | no | Current recognition Block UUID; stale after re-recognition. |
| `section` | no | Advisory source-section hint. |

Use `document/{uuid}` instead of `self` only for an explicit cross-document
comparison. A single-paper note normally uses `self`.

## How to obtain a target

- Source page and section: `papers_manager_document_structure`.
- Block UUID, page dimensions, bbox: `papers_manager_document_read_blocks`.
- Normalize a block rectangle as:
  - `x0 = bboxX0 / pageWidth`
  - `y0 = bboxY0 / pageHeight`
  - `x1 = bboxX1 / pageWidth`
  - `y1 = bboxY1 / pageHeight`
- Validate the final link with `papers_manager_evidence_validate` before writing it.

Do not guess page numbers from section numbers, and do not fabricate a Block
UUID. If precise coordinates are unavailable, a page-only link is valid and
still navigable.

## Placement

Place evidence next to the claim it supports:

```markdown
训练目标是最小化重构误差。[第 5 页](papers-manager://self/evidence?v=1&page=5&rect=0.12,0.25,0.86,0.48)
```

- One link may support a short claim or a small group of tightly related
  claims.
- Do not copy the source excerpt into the note; the selected text stays in the
  PDF.
- Do not put evidence links only in a section description. Descriptions are
  navigation metadata; claims need body-level links.
- For a section-level mapping, the description may say
  `对应原文 §3，pp.4–6`, but this does not replace body links.

## Missing evidence

When a claim is useful but cannot be tied to the source, mark it explicitly:

```markdown
> 证据缺口：当前 source section 没有给出对应的页码或 Block。
```

Do not invent a page/rect/Block to satisfy the format. It is better to keep an
honest gap than a false precision.

## Base comparison

In a Base summary, every material comparison should have:

- one evidence link per paper claim, or
- an explicit evidence gap for that claim.

Cross-document links are allowed only when the comparison is explicitly about
another document. Page/rectangle remains the fallback when a Block hint becomes
stale after recognition.

## Anti-patterns

- Copying the selected paper text into the note.
- Using a section hint as a substitute for page/rectangle.
- Reusing a Block UUID from an older recognition generation without checking.
- Writing a link whose page is outside the document.
- Putting all evidence in a separate table and leaving claims unlinked.
