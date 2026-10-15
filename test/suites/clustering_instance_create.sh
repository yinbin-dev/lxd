test_clustering_instance_create() {
  # shellcheck disable=SC2154
  local bridge="${bridge}"

  spawn_lxd_and_bootstrap_cluster

  local cert
  cert="$(cert_to_yaml "${LXD_ONE_DIR}/cluster.crt")"

  spawn_lxd_and_join_cluster "${cert}" 2 1 "${LXD_ONE_DIR}"
  spawn_lxd_and_join_cluster "${cert}" 3 1 "${LXD_ONE_DIR}"

  local dirs=("${LXD_ONE_DIR}" "${LXD_TWO_DIR}" "${LXD_THREE_DIR}")

  sub_test "Create an instance from an image on a member that is not the receiver"
  LXD_DIR="${LXD_TWO_DIR}" ensure_import_testimage
  LXD_DIR="${LXD_ONE_DIR}" lxc init testimage c-image --target node3
  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc list -f csv -c nsL c-image)" = "c-image,STOPPED,node3" ]
  fingerprint="$(LXD_DIR="${LXD_ONE_DIR}" lxc image info testimage | awk '/^Fingerprint:/ {print $2}')"
  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc config get c-image volatile.base_image)" = "${fingerprint}" ]
  LXD_DIR="${LXD_THREE_DIR}" lxc start c-image
  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc list -f csv -c s c-image)" = "RUNNING" ]
  LXD_DIR="${LXD_ONE_DIR}" lxc delete --force c-image

  sub_test "Create an empty instance on a member that is not the receiver"
  LXD_DIR="${LXD_ONE_DIR}" lxc init --empty c-empty --target node2
  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc list -f csv -c nsL c-empty)" = "c-empty,STOPPED,node2" ]
  LXD_DIR="${LXD_TWO_DIR}" lxc storage volume show data container/c-empty --target node2 >/dev/null

  sub_test "A name that is in use fails in the receiver transaction"
  local exit_code=0
  err_msg="$(LXD_DIR="${LXD_THREE_DIR}" lxc init testimage c-empty 2>&1)" || exit_code=$?
  [ "${exit_code}" -ne 0 ]
  [[ "${err_msg}" == *'Instance "c-empty" already exists'* ]]
  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc list -f csv -c L c-empty)" = "node2" ]
  LXD_DIR="${LXD_ONE_DIR}" lxc delete c-empty

  sub_test "An evacuated target fails before the receiver writes the instance row"
  LXD_DIR="${LXD_ONE_DIR}" lxc cluster evacuate node2 --yes
  exit_code=0
  err_msg="$(LXD_DIR="${LXD_ONE_DIR}" lxc init --empty c-rejected --target node2 2>&1)" || exit_code=$?
  [ "${exit_code}" -ne 0 ]
  [[ "${err_msg}" == *"Cluster member is evacuated"* ]]
  if LXD_DIR="${LXD_ONE_DIR}" lxc info c-rejected >/dev/null 2>&1; then
    echo "ERROR: The receiver wrote the instance row for an evacuated target"
    exit 1
  fi

  LXD_DIR="${LXD_ONE_DIR}" lxc cluster restore node2 --force

  sub_test "Concurrent creates cannot go above limits.instances"
  local n=6
  LXD_DIR="${LXD_ONE_DIR}" lxc project create limited -c features.images=false -c features.profiles=false -c limits.instances=$((n - 1))
  local pids=()
  for i in $(seq 1 "${n}"); do
    LXD_DIR="${dirs[$((i % 3))]}" lxc init --empty "c-limit-${i}" --project limited > "${TEST_DIR}/c-limit-${i}.log" 2>&1 &
    pids+=("$!")
  done

  local failed=0
  for pid in "${pids[@]}"; do
    wait "${pid}" || failed=$((failed + 1))
  done

  [ "${failed}" -eq 1 ]
  grep -hF 'Reached maximum number of instances in project "limited"' "${TEST_DIR}"/c-limit-*.log
  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc list -f csv -c n --project limited | wc -l)" -eq $((n - 1)) ]
  for i in $(seq 1 "${n}"); do
    LXD_DIR="${LXD_ONE_DIR}" lxc delete "c-limit-${i}" --project limited 2>/dev/null || true
  done

  LXD_DIR="${LXD_ONE_DIR}" lxc project delete limited

  sub_test "Concurrent creates in a strict spread placement group go to different members"
  LXD_DIR="${LXD_ONE_DIR}" lxc placement-group create pg-strict policy=spread rigor=strict
  pids=()
  for i in 1 2 3; do
    LXD_DIR="${dirs[$((i - 1))]}" lxc init --empty "c-pg-${i}" -c placement.group=pg-strict &
    pids+=("$!")
  done

  for pid in "${pids[@]}"; do
    wait "${pid}"
  done

  [ "$(LXD_DIR="${LXD_ONE_DIR}" lxc list -f csv -c L c-pg- | sort -u | wc -l)" -eq 3 ]
  LXD_DIR="${LXD_ONE_DIR}" lxc delete c-pg-1 c-pg-2 c-pg-3
  LXD_DIR="${LXD_ONE_DIR}" lxc placement-group delete pg-strict

  if [ "${LXD_VM_TESTS}" = "1" ]; then
    sub_test "A VM row without a root volume record can be deleted"
    LXD_DIR="${LXD_ONE_DIR}" lxc init --empty --vm v-novol --target node2 --device "${SMALL_ROOT_DISK}"
    # An uncertain handoff leaves an instance row without a root volume record.
    LXD_DIR="${LXD_ONE_DIR}" lxd sql global "DELETE FROM storage_volumes WHERE name='v-novol' AND type=3 AND node_id=(SELECT id FROM nodes WHERE name='node2')"
    LXD_DIR="${LXD_ONE_DIR}" lxc delete v-novol
    if LXD_DIR="${LXD_ONE_DIR}" lxc info v-novol >/dev/null 2>&1; then
      echo "ERROR: The VM row without a root volume record stays after the delete"
      exit 1
    fi
  fi

  sub_test "Clean up"
  LXD_DIR="${LXD_ONE_DIR}" lxc image delete testimage
  LXD_DIR="${LXD_THREE_DIR}" lxd shutdown
  LXD_DIR="${LXD_TWO_DIR}" lxd shutdown
  LXD_DIR="${LXD_ONE_DIR}" lxd shutdown

  rm -f "${LXD_THREE_DIR}/unix.socket"
  rm -f "${LXD_TWO_DIR}/unix.socket"
  rm -f "${LXD_ONE_DIR}/unix.socket"

  teardown_clustering_netns
  teardown_clustering_bridge

  kill_lxd "${LXD_ONE_DIR}"
  kill_lxd "${LXD_TWO_DIR}"
  kill_lxd "${LXD_THREE_DIR}"
}
