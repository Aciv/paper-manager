"""Transition-safe decoding and metrics for the joint reference-layout CRF."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Sequence

from .schema import LayoutLine, ReferenceEntry


REFERENCE_LABELS = frozenset({"REF_HEAD", "B-REF", "I-REF"})
_ORDER = ("PRE", "REF_HEAD", "B-REF", "I-REF", "POST")


@dataclass(frozen=True)
class ReferenceLayoutResult:
    """Decoded bibliography region and individual contiguous entries."""

    labels: tuple[str, ...]
    region_start: int | None
    region_end: int | None
    entries: tuple[ReferenceEntry, ...]


def legalize_reference_labels(labels: Sequence[str]) -> tuple[str, ...]:
    """Enforce PRE → reference-zone → POST without inventing extra entries."""
    state = "PRE"
    output: list[str] = []
    for raw in labels:
        label = raw if raw in _ORDER else "PRE"
        if state == "PRE":
            if label == "REF_HEAD":
                state = "REF_HEAD"
            elif label in {"B-REF", "I-REF"}:
                label = "B-REF"
                state = "B-REF"
            else:
                label = "PRE"
        elif state in {"REF_HEAD", "B-REF", "I-REF"}:
            if label == "POST":
                state = "POST"
            elif label == "PRE":
                # A return to PRE after a bibliography begins is almost always
                # a CRF transition error; retain it as a reference continuation.
                label = "I-REF"
                state = "I-REF"
            elif label == "REF_HEAD":
                # A second heading cannot start a second zone; continue instead.
                label = "I-REF"
                state = "I-REF"
            else:
                state = label
        else:
            label = "POST"
        output.append(label)
    return tuple(output)


def decode_reference_layout(document_id: str, lines: Sequence[LayoutLine], labels: Sequence[str]) -> ReferenceLayoutResult:
    """Validate labels and assemble entries from `B-REF`/`I-REF` runs."""
    if len(lines) != len(labels):
        raise ValueError("reference labels must match line count")
    normalized = legalize_reference_labels(labels)
    reference_indexes = [index for index, label in enumerate(normalized) if label in REFERENCE_LABELS]
    region_start = reference_indexes[0] if reference_indexes else None
    region_end = reference_indexes[-1] if reference_indexes else None
    entries: list[ReferenceEntry] = []
    current: list[int] = []
    for index, label in enumerate(normalized):
        if label == "B-REF":
            if current:
                entries.append(_entry(document_id, len(entries), lines, current))
            current = [index]
        elif label == "I-REF" and current:
            current.append(index)
        elif label == "I-REF":
            # A bare I after a heading is a valid unnumbered first entry.
            current = [index]
        elif current:
            entries.append(_entry(document_id, len(entries), lines, current))
            current = []
    if current:
        entries.append(_entry(document_id, len(entries), lines, current))
    return ReferenceLayoutResult(normalized, region_start, region_end, tuple(entries))


def _entry(document_id: str, index: int, lines: Sequence[LayoutLine], positions: list[int]) -> ReferenceEntry:
    return ReferenceEntry(
        document_id=document_id,
        entry_index=index,
        line_orders=tuple(lines[position].order_in_document for position in positions),
        raw_text="\n".join(lines[position].text for position in positions),
    )


def reference_metrics(lines: Sequence[LayoutLine], expected: Sequence[str], predicted: Sequence[str]) -> dict[str, float]:
    """Compute Phase-21 line, region, and entry-boundary metrics for one PDF."""
    truth = decode_reference_layout(lines[0].document_id if lines else "empty", lines, expected)
    output = decode_reference_layout(lines[0].document_id if lines else "empty", lines, predicted)
    truth_region = {index for index, label in enumerate(truth.labels) if label in REFERENCE_LABELS}
    output_region = {index for index, label in enumerate(output.labels) if label in REFERENCE_LABELS}
    intersection = len(truth_region & output_region)
    union = len(truth_region | output_region)
    true_boundaries = {index for index, label in enumerate(truth.labels) if label == "B-REF"}
    predicted_boundaries = {index for index, label in enumerate(output.labels) if label == "B-REF"}
    exact_truth = {entry.line_orders for entry in truth.entries}
    exact_predicted = {entry.line_orders for entry in output.entries}
    return {
        "region_tp": float(intersection),
        "region_predicted": float(len(output_region)),
        "region_expected": float(len(truth_region)),
        "region_iou": intersection / union if union else 1.0,
        "start_acc_at_1": float(_boundary_close(truth.region_start, output.region_start)),
        "end_acc_at_1": float(_boundary_close(truth.region_end, output.region_end)),
        "boundary_tp": float(len(true_boundaries & predicted_boundaries)),
        "boundary_predicted": float(len(predicted_boundaries)),
        "boundary_expected": float(len(true_boundaries)),
        "exact_tp": float(len(exact_truth & exact_predicted)),
        "exact_predicted": float(len(exact_predicted)),
        "exact_expected": float(len(exact_truth)),
    }


def _boundary_close(expected: int | None, predicted: int | None) -> bool:
    return expected is None and predicted is None or (
        expected is not None and predicted is not None and abs(expected - predicted) <= 1
    )
