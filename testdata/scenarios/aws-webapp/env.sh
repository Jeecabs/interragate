# Sourced by scripts/gen-fixture.sh: start a throwaway moto server.
export AWS_ACCESS_KEY_ID=fixture AWS_SECRET_ACCESS_KEY=fixture AWS_REGION=us-east-1 AWS_DEFAULT_REGION=us-east-1
moto_port=${MOTO_PORT:-5055}
export AWS_ENDPOINT_URL=http://127.0.0.1:$moto_port
uvx --python 3.12 --from 'moto[server]==5.*' moto_server -p "$moto_port" >/tmp/interragate-moto.log 2>&1 &
moto_pid=$!
scenario_cleanup() { kill "$moto_pid" 2>/dev/null || true; }
for _ in $(seq 1 60); do
  curl -sf "$AWS_ENDPOINT_URL/moto-api/" >/dev/null 2>&1 && break
  sleep 0.5
done
