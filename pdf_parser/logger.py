import logging
import os
import sys
from pathlib import Path
from logging.handlers import TimedRotatingFileHandler

_LOGERS: dict[str, logging.Logger] = {}
_DEFAULT_LOG_PATH = Path(__file__).resolve().parent / "Logs" / "today.log"
_HANDLER: logging.Handler | None = None


def _resolve_log_path() -> Path:
    """Return the optional file target selected by ``LOG_PATH``."""
    configured = os.getenv("LOG_PATH", "").strip()
    return Path(configured).expanduser() if configured else _DEFAULT_LOG_PATH


def _build_handler(level: int) -> logging.Handler:
    """Create a file handler when possible, otherwise log to container stdout.

    The parser image intentionally uses a read-only application filesystem.
    A delayed ``TimedRotatingFileHandler`` only discovers that restriction on
    its first emit, which used to produce a logging traceback during RPCs.
    Probe the target eagerly and fall back to stdout so Docker logs remain the
    reliable operational sink without affecting parser availability.
    """
    path = _resolve_log_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8"):
            pass
        handler: logging.Handler = TimedRotatingFileHandler(
            filename=path,
            when="midnight",
            encoding="utf-8",
            backupCount=30,
            delay=True,
        )
        handler.rotate = lambda src, dst: _safe_rotate(src, dst)  # type: ignore[method-assign]
    except OSError:
        # Read-only containers and restricted runtime users must never lose a
        # recognition request merely because a local log file is unavailable.
        handler = logging.StreamHandler(sys.stdout)
    handler.setLevel(level)
    handler.setFormatter(
        logging.Formatter(
            "[%(asctime)s] %(levelname)-5s %(filename)s:%(lineno)d — %(message)s",
            datefmt="%Y-%m-%d %H:%M:%S",
        )
    )
    return handler


def _safe_rotate(source: str, dest: str):
    """Windows-friendly rotate that tolerates locked files."""
    try:
        os.rename(source, dest)
    except PermissionError:
        # File is locked by another process (IDE, antivirus, old instance).
        # On Windows this is common — skip the rotate rather than crashing.
        sys.stderr.write(
            f"[logger] Cannot rotate {source} → {dest} — file is locked. Skipping rotation.\n"
        )


def get_logger(name: str, level: int = logging.INFO) -> logging.Logger:
    global _HANDLER

    if name in _LOGERS:
        return _LOGERS[name]

    logger = logging.getLogger(f"today.{name}")
    logger.setLevel(level)
    # The managed rotating file handler is the single output for this logger.
    # Avoid forwarding records to a root handler configured by an embedding
    # process, which would otherwise duplicate parser log entries.
    logger.propagate = False

    if _HANDLER is None:
        _HANDLER = _build_handler(level)

    logger.addHandler(_HANDLER)

    _LOGERS[name] = logger
    return logger
