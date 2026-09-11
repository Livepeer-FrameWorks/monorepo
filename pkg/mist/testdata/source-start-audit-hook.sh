#!/bin/sh
IFS= read -r stream_name
printf '%s\n' "$stream_name" >> /tmp/source-start-audit
printf 'dtsc://127.0.0.1:14200/origin\n'
