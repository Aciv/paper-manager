"""Pure inference code for the paper CRF cascade.

These modules are the released feature, schema, and decoding implementations
used by the parser. ``model_train`` is the internal training workspace and
keeps its own copy; keep the two in sync when promoting an artifact. Nothing
in this package may import ``model_train``.
"""
