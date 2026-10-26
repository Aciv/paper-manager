#!/bin/sh
set -eu
directory=${0%/*}
directory=$(CDPATH= cd -- "$directory" && pwd)
exec "$directory/bundle/python/bin/python3" -I -X utf8 -B "$directory/installer/install.py" uninstall "$@"
