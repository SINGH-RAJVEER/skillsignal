#!/bin/sh
set -eu

if [ "$#" -gt 0 ]; then
	exec /skillsignal-api "$@"
fi

/skillsignal-api migrate
exec /skillsignal-api serve
