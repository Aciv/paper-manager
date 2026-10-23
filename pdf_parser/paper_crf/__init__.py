"""Runtime-owned paper CRF stack.

The package has three parts:

``models/``
    Pure feature extraction, schema, and decoding code used at inference.
``weights/``
    Trained CRF parameter files (``.crfsuite``).
``specs/``
    Per-artifact manifests plus ``bundle.json``, which selects the exact
    weights/spec pair for each runtime role.

``model_train/`` is the internal training workspace and is never imported at
runtime. This package is the released copy; keep its feature code in sync when
promoting a newly trained artifact.
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

PACKAGE_ROOT = Path(__file__).resolve().parent
BUNDLE_SCHEMA_VERSION = "paper-crf-bundle/v1"
ROLES = ("header", "reference", "citation")


class BundleError(ValueError):
    """The runtime CRF bundle is missing, incomplete, or malformed."""


@dataclass(frozen=True)
class ModelSelection:
    """One runtime role mapped to its weights and manifest."""

    role: str
    weights: Path
    spec: Path


@dataclass(frozen=True)
class RuntimeSettings:
    """Calibrated thresholds and layout switches owned by the bundle spec."""

    header_threshold: float
    reference_threshold: float
    header_use_layout: bool
    reference_use_layout: bool


@dataclass(frozen=True)
class Bundle:
    """The selected runtime CRF stack, resolved against a bundle root."""

    root: Path
    models: dict[str, ModelSelection]
    runtime: RuntimeSettings


def load_bundle(
    root: str | Path | None = None,
    *,
    require_reference: bool = True,
) -> Bundle:
    """Load and validate ``<root>/specs/bundle.json``.

    ``root`` defaults to this package. Runtime callers pass the optional
    ``PAPER_CRF_MODEL_DIR`` override so an experimental promoted bundle can be
    tested without replacing the packaged one. When the reference-region CRF
    is disabled, ``require_reference=False`` lets a bundle omit that role (the
    packaged bundle keeps it for future experiments).
    """
    bundle_root = Path(root).expanduser().resolve() if root is not None else PACKAGE_ROOT
    bundle_path = bundle_root / "specs" / "bundle.json"
    if not bundle_path.is_file():
        raise BundleError(f"CRF bundle manifest is missing: {bundle_path}")
    try:
        payload = json.loads(bundle_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise BundleError(f"CRF bundle manifest is unreadable: {bundle_path}: {exc}") from exc
    if not isinstance(payload, dict):
        raise BundleError("CRF bundle manifest must be a JSON object")
    if payload.get("schema_version") != BUNDLE_SCHEMA_VERSION:
        raise BundleError(
            f"CRF bundle schema mismatch: {payload.get('schema_version')!r} "
            f"!= {BUNDLE_SCHEMA_VERSION!r}"
        )

    models_payload = payload.get("models")
    if not isinstance(models_payload, dict):
        raise BundleError("CRF bundle models must be a JSON object")
    models: dict[str, ModelSelection] = {}
    required_roles = ROLES if require_reference else tuple(
        role for role in ROLES if role != "reference"
    )
    for role in required_roles:
        entry = models_payload.get(role)
        if not isinstance(entry, dict):
            raise BundleError(f"CRF bundle is missing model role {role!r}")
        models[role] = ModelSelection(
            role=role,
            weights=_resolve_member(bundle_root, entry.get("weights"), f"models.{role}.weights"),
            spec=_resolve_member(bundle_root, entry.get("spec"), f"models.{role}.spec"),
        )

    runtime_payload = payload.get("runtime")
    if not isinstance(runtime_payload, dict):
        raise BundleError("CRF bundle runtime must be a JSON object")
    runtime = RuntimeSettings(
        header_threshold=_ratio(runtime_payload.get("header_threshold"), "runtime.header_threshold"),
        reference_threshold=_ratio(
            runtime_payload.get("reference_threshold"), "runtime.reference_threshold"
        ),
        header_use_layout=_flag(
            runtime_payload.get("header_use_layout"), "runtime.header_use_layout"
        ),
        reference_use_layout=_flag(
            runtime_payload.get("reference_use_layout"), "runtime.reference_use_layout"
        ),
    )
    return Bundle(root=bundle_root, models=models, runtime=runtime)


def _resolve_member(root: Path, value: Any, name: str) -> Path:
    if not isinstance(value, str) or not value.strip():
        raise BundleError(f"{name} must be a non-empty relative path")
    path = (root / value).expanduser().resolve()
    try:
        path.relative_to(root)
    except ValueError as exc:
        raise BundleError(f"{name} escapes the bundle root: {value!r}") from exc
    if not path.is_file():
        raise BundleError(f"{name} is missing: {path}")
    return path


def _ratio(value: Any, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise BundleError(f"{name} must be a number in [0, 1]")
    number = float(value)
    if not 0.0 <= number <= 1.0:
        raise BundleError(f"{name} must be a number in [0, 1]")
    return number


def _flag(value: Any, name: str) -> bool:
    if not isinstance(value, bool):
        raise BundleError(f"{name} must be a boolean")
    return value
