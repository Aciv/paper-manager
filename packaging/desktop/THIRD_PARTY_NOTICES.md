# Internal validation dependency inventory

These are unsigned installation/validation candidates.
Project-owned code is licensed under MIT; see licenses/paper_manager-MIT.txt.
Third-party components retain their own terms. The maintainer authorizes the two
bundled CRF weights; no training dataset is shipped or requested. Public binary
redistribution checks remain separate from the project source-code license.

The build preserves upstream notices under `licenses/`, Python package metadata,
Go module identities, resolved wheel hashes, the source archive lock and a
CycloneDX component inventory. Automated collection is not a complete legal audit.
The component inventory includes frontend build dependencies; presence there
alone does not mean that executable dependency is shipped.

| Component | Internal candidate | Terms / remaining public distribution work |
| --- | --- | --- |
| CPython standalone | 3.12.15 / build 20261021 | Retain bundled CPython and native library license texts; audit exact target archives. |
| PostgreSQL | 17.11 | PostgreSQL License; retain COPYRIGHT and audit dependencies of the Windows EDB ZIP. |
| Windows MSVC runtime | From pinned CPython/NumPy inputs | Private PostgreSQL DLL copies retain their bytes and record provenance; audit Microsoft redistribution notices before public distribution. |
| Redis | 7.4.11 | RSALv2 OR SSPLv1; select a compliant route before public distribution. This version is not BSD licensed. |
| Windows Redis DLLs | Candidate ZIP pinned in sources.lock.json | Includes Cygwin and other libraries. Collect exact DLL identities, license texts and corresponding source material before public distribution. The unused .NET service wrapper is excluded. |
| PyMuPDF / PyMuPDF4LLM | 1.28.0 | AGPLv3 or Artifex commercial terms; no commercial license is assumed. |
| PyMuPDF Layout | 1.28.0 | PolyForm Noncommercial or Artifex commercial terms in the currently pinned wheel; do not infer newer releases' license terms. |
| CRF models | Optional hybrid profile; hashes in dependencies.json | Header/citation weights are maintainer-authorized bundled resources, identified by their hashes; no training dataset provenance is required. The rules profile excludes weights. |
| Frontend fonts | Versions from web/package-lock.json | Preserve upstream font notices; audit the final web assets and font subsets. |
| Other Go / Python / JS packages | Exact identities in the bundle | Preserve supplied notices; resolve missing license/source obligations before public distribution. |

Authoritative references:

- [PostgreSQL License](https://www.postgresql.org/about/licence/)
- [Redis version-specific licensing](https://redis.io/legal/licenses/)
- [Cygwin licensing](https://cygwin.com/licensing.html)
- [PyMuPDF licensing](https://pymupdf.io/licensing)
- [Standalone CPython release](https://github.com/astral-sh/python-build-standalone/releases/tag/20261021)

Public-release blockers: Redis license route and
Windows DLL/source notices incomplete; current Layout terms unresolved for general
redistribution; third-party font notices and complete transitive audit
pending. Signing, installers, upgrades and uninstall behavior belong to D4.
