#!/bin/sh
IFS= read -r stream_name
IFS= read -r client_address
IFS= read -r viewer_token
if [ "$stream_name" = protected-replica ] && [ "$viewer_token" = fixture-viewer-token ]; then
  printf 'allow\n' >> /tmp/viewer-credentials-audit
  printf 'true\n'
else
  printf 'deny\n' >> /tmp/viewer-credentials-audit
  printf 'false\n'
fi
