#!/bin/bash
set -e

echo "Killing existing instances..."
killall main || true
killall server || true
sleep 1

echo "Starting backends..."
go run backend/server.go 8001 10 Alpha-Fast &
PID1=$!
go run backend/server.go 8002 10 Beta-Admin &
PID2=$!
go run backend/server.go 8003 10 Gamma-All &
PID3=$!
go run backend/server.go 8004 10 Delta-All &
PID4=$!

echo "Starting load balancer..."
go run cmd/loadbalancer/main.go &
LBPID=$!

sleep 3

echo "Testing /api/payment (Should go to Alpha-Fast :8001)"
curl -s http://localhost:8080/api/payment | grep "Alpha-Fast" || echo "FAILED"

echo "Testing /admin with X-Admin-Key (Should go to Beta-Admin :8002)"
curl -s -H "X-Admin-Key: secret" http://localhost:8080/admin | grep "Beta-Admin" || echo "FAILED"

echo "Testing /admin WITHOUT X-Admin-Key (Should fall through to all-backends :8003/8004)"
curl -s http://localhost:8080/admin | grep -E "Gamma-All|Delta-All" || echo "FAILED"

echo "Testing /other (Should fall through to all-backends :8003/8004)"
curl -s http://localhost:8080/other | grep -E "Gamma-All|Delta-All" || echo "FAILED"

echo "Shutting down..."
kill $LBPID $PID1 $PID2 $PID3 $PID4
wait $LBPID 2>/dev/null || true
echo "Done"
