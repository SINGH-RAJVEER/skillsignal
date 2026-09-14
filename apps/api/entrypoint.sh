#!/bin/sh
set -eu

/skillsignal-api migrate
exec /skillsignal-api serve
