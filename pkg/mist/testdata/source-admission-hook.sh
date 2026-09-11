#!/bin/sh
IFS= read -r stream_name
case "$stream_name" in
  allowed-origin) printf 'true\n' ;;
  *) printf 'false\n' ;;
esac
