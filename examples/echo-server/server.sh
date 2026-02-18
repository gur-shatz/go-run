#!/bin/sh

PORT="${PORT:-9090}"
echo "echo-server listening on :$PORT"
while true; do
  echo "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\necho: $(date)" | nc -l "$PORT" > /dev/null 2>&1
done
