#!/bin/sh
# Container entrypoint: dispatches to the API server (default) or to the
# migration tool, so one image serves both paths:
#
#   docker run meupreco              -> API server
#   docker run meupreco migrate up   -> applies the embedded migrations
#
# Any other argument is passed straight to the server.
set -eu

if [ "$#" -eq 0 ]; then
    exec /usr/local/bin/server
fi

command="$1"
shift

case "$command" in
    server)
        exec /usr/local/bin/server "$@"
        ;;
    migrate)
        exec /usr/local/bin/migrate "$@"
        ;;
    *)
        exec /usr/local/bin/server "$command" "$@"
        ;;
esac
