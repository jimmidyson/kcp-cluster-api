#!/bin/sh
# The probe the kubelet makes, against every manager's health address.
set -eu
for port in 9441 9442 9443 9450; do
  for path in /healthz /readyz; do
    code=$(curl -s -o /dev/null -w "%{http_code}" --noproxy '*' "http://localhost:$port$path" || echo "no answer")
    echo "$port$path -> $code"
  done
done
