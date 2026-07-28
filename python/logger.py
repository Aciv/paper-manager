import logging
import os
import sys
from logging.handlers import TimedRotatingFileHandler

_LOGERS: dict[str, logging.Logger] = {}
_LOG_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "Logs")
_HANDLER: TimedRotatingFileHandler | None = None


def _ensure_log_dir():
    os.makedirs(_LOG_DIR, exist_ok=True)


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
        _ensure_log_dir()

        _HANDLER = TimedRotatingFileHandler(
            filename=os.path.join(_LOG_DIR, "today.log"),
            when="midnight",
            encoding="utf-8",
            backupCount=30,
            delay=True,
        )
        _HANDLER.setLevel(level)
        _HANDLER.setFormatter(
            logging.Formatter(
                "[%(asctime)s] %(levelname)-5s %(filename)s:%(lineno)d — %(message)s",
                datefmt="%Y-%m-%d %H:%M:%S",
            )
        )
        _HANDLER.rotate = lambda src, dst: _safe_rotate(src, dst)  # type: ignore[method-assign]

    logger.addHandler(_HANDLER)

    _LOGERS[name] = logger
    return logger
