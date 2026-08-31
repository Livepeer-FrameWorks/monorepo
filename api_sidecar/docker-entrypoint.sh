#!/bin/sh
set -eu

: "${HELMSMAN_STATE_DIR:?HELMSMAN_STATE_DIR is required}"

case "${HELMSMAN_STATE_DIR}" in
    /var/lib/frameworks/* | /data/*) ;;
    *)
        echo "HELMSMAN_STATE_DIR must be below /var/lib/frameworks or /data" >&2
        exit 1
        ;;
esac
if [ -L "${HELMSMAN_STATE_DIR}" ]; then
    echo "HELMSMAN_STATE_DIR must not be a symbolic link" >&2
    exit 1
fi

install -d -o frameworks -g frameworks -m 0700 "${HELMSMAN_STATE_DIR}"
chown frameworks:frameworks "${HELMSMAN_STATE_DIR}"
chmod 0700 "${HELMSMAN_STATE_DIR}"

exec su-exec frameworks:frameworks "$@"
