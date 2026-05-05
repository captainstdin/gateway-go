#!/bin/bash

# Start register, worker, and gateway services

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "Starting register..."
"$SCRIPT_DIR/register/register" &
REGISTER_PID=$!

echo "Starting worker..."
"$SCRIPT_DIR/worker/worker" &
WORKER_PID=$!

echo "Starting gateway..."
"$SCRIPT_DIR/gateway/gateway" &
GATEWAY_PID=$!

echo ""
echo "All services started:"
echo "  register  PID=$REGISTER_PID"
echo "  worker    PID=$WORKER_PID"
echo "  gateway   PID=$GATEWAY_PID"
echo ""
echo "Press Ctrl+C to stop all services."

# Trap SIGINT/SIGTERM to kill all child processes
trap 'echo "Stopping all services..."; kill $REGISTER_PID $WORKER_PID $GATEWAY_PID 2>/dev/null; wait; echo "Done."; exit 0' INT TERM

# Wait for all background processes
wait
